package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
)

// Attaching to a run splits the work between two goroutines. The run
// observer owns the network: it polls the run tree and the log tails its
// frame shows, and publishes both as one immutable snapshot. The calling
// goroutine owns the terminal: it alone touches the renderer and its
// footer and consumes keys and signals, so a slow request never stalls the
// spinner, the elapsed timers, or a detach, and the two goroutines share
// nothing that needs a lock.

// runAttachHint renders the reattach command for a run, carrying the
// invocation's explicit --remote so the hint points at the same master.
func runAttachHint(remote, runID string) string {
	if remote == "" {
		return "skali run attach " + runID
	}
	return "skali run attach --remote " + remote + " " + runID
}

// attachMode decides what Ctrl-C means while attached to a run.
type attachMode int

const (
	// attachCancelsRun is the deploy-like attach: the local work is done and
	// the run continues on the server, so d detaches and Ctrl-C, pressed
	// twice, cancels the run. Without a terminal an interrupt detaches.
	attachCancelsRun attachMode = iota
	// attachSessionEnds is the dev session's attach: the caller owns the
	// signal (Ctrl-C ends the session and pauses the project), so an
	// interrupt only ends the wait; d still detaches.
	attachSessionEnds
)

// interruptCancelWindow is how long a first Ctrl-C stays armed for the
// second press that cancels the run.
const interruptCancelWindow = 3 * time.Second

// The observer polls the server twice a second; the spinner ticks faster
// so the display stays visibly alive between snapshots.
const (
	attachPollInterval  = 500 * time.Millisecond
	attachFrameInterval = 150 * time.Millisecond
)

// attachOutcome is how an attached run ended: its final status, or
// "detached" / "interrupted" when the attachment ended before the run did.
// Failure carries a failed run's one-line reason for the closing line.
type attachOutcome struct {
	Status  string
	Failure string
}

// failedRunError is the closing line of a command whose run failed: the run
// id and, when the run recorded one, its reason.
func failedRunError(runID, failure string) error {
	if failure == "" {
		return fmt.Errorf("run %s failed", runID)
	}
	return fmt.Errorf("run %s failed: %s", runID, failure)
}

// attachRun renders the run tree until it settles or the user detaches
// with an interrupt (detaching never cancels). A parent context dying under
// the wait (a deadline, a lost session) is not a detach: nobody asked for
// one, so it returns "interrupted" without claiming anything. remoteHint
// names the invocation's explicit --remote for the reattach hint; empty
// means the default resolution finds the run again. The key bindings are
// the deploy-like ones; see attachRunMode.
func attachRun(ctx context.Context, out io.Writer, api *client.Client, runID, remoteHint string) (attachOutcome, error) {
	outcome, err := attachRunMode(ctx, out, api, runID, remoteHint, attachCancelsRun)
	if err != nil {
		return outcome, fmt.Errorf("observation of run %s ended: %w; reconnect with %s", runID, err, runAttachHint(remoteHint, runID))
	}
	return outcome, nil
}

// cancelReply is the server's answer to the attachment's cancel request.
type cancelReply struct {
	outcome *client.CancelOutcome
	err     error
}

// attachRunMode renders a run's step tree live until the run ends, the
// user detaches, or, in the cancel mode, asks for the run to be cancelled.
// The outcome's status is the run's final status, "detached", or
// "interrupted" (the parent context ended).
func attachRunMode(ctx context.Context, out io.Writer, api *client.Client, runID, remoteHint string, mode attachMode) (attachOutcome, error) {
	tty := clirender.IsTerminal(out)
	style := clirender.StyleFor(out)
	// shown is the snapshot on screen; the renderer reads its tails on
	// every frame.
	var shown runSnapshot
	renderer := &clirender.Renderer{
		Out:   out,
		TTY:   tty,
		Style: style,
		Logs:  func(stepID string) []string { return shown.tails[stepID] },
	}

	// In the session mode the interrupt ends the wait through the context;
	// in the cancel mode it is a key press to interpret.
	attachCtx, stop := ctx, func() {}
	interrupts := make(chan os.Signal, 2)
	if mode == attachSessionEnds {
		attachCtx, stop = signal.NotifyContext(ctx, os.Interrupt)
	} else {
		signal.Notify(interrupts, os.Interrupt)
		defer signal.Stop(interrupts)
	}
	defer stop()
	var keys <-chan byte
	release := func() {}
	if tty {
		keys, release = terminalKeys.subscribe()
	}
	defer release()

	// The observation ends with the attachment, whichever way it ends: a
	// detach or a session interrupt must not leave a poll in flight, and
	// the observer has returned before this function does.
	observeCtx, endObservation := context.WithCancel(attachCtx)
	snapshots := observeRun(observeCtx, api, runID, style, verboseTranscript)
	defer func() {
		endObservation()
		for range snapshots {
		}
	}()

	hint := attachHint(mode, style)
	if tty {
		renderer.Footer = hint
	}
	var disarm <-chan time.Time
	cancelRequested := false
	// cancelReplies is non-nil while the cancel request is in flight. A
	// requested cancellation is delivered even when the user detaches
	// before the server answers.
	var cancelReplies chan cancelReply
	defer func() {
		if cancelReplies != nil {
			<-cancelReplies
		}
	}()
	// continues: the cancelled run was a first deployment, whose revision
	// stays the target with no earlier one to return to.
	continues := false
	// takeCancelReply records the server's answer; a run that finished
	// before the request landed is not an error.
	takeCancelReply := func(reply cancelReply) error {
		cancelReplies = nil
		if reply.err != nil && !isRunAlreadyFinished(reply.err) {
			return reply.err
		}
		continues = reply.outcome != nil && reply.outcome.Continues
		return nil
	}

	spin := time.NewTicker(attachFrameInterval)
	defer spin.Stop()
	if !tty {
		spin.Stop()
	}

	kind := "run"
	detach := func() (attachOutcome, error) {
		renderer.Detach()
		fmt.Fprintf(out, "\ndetached from run %s; the %s continues on the server\n", runID, kind)
		fmt.Fprintf(out, "  reattach  %s\n", runAttachHint(remoteHint, runID))
		return attachOutcome{Status: "detached"}, nil
	}
	// ended handles the attachment's context ending: the session's
	// interrupt detaches, the parent's end interrupts.
	ended := func() (attachOutcome, error) {
		renderer.Detach()
		if ctx.Err() != nil {
			return attachOutcome{Status: "interrupted"}, nil
		}
		return detach()
	}
	// interrupt handles Ctrl-C in the cancel mode: the first press arms,
	// the second within the window cancels the run, and once the
	// cancellation is requested a further press detaches from the wait.
	// The cancel request runs beside the loop, which keeps animating and
	// takes the answer when it arrives.
	interrupt := func() (done bool, status string, err error) {
		if !tty {
			outcome, err := detach()
			return true, outcome.Status, err
		}
		switch {
		case cancelRequested:
			outcome, err := detach()
			return true, outcome.Status, err
		case disarm != nil:
			cancelRequested = true
			disarm = nil
			renderer.Footer = style.Yellow("cancelling the run, waiting for the server")
			cancelReplies = make(chan cancelReply, 1)
			go func(replies chan<- cancelReply) {
				outcome, err := api.CancelRun(ctx, runID)
				replies <- cancelReply{outcome, err}
			}(cancelReplies)
		default:
			disarm = time.After(interruptCancelWindow)
			renderer.Footer = style.Yellow("press Ctrl-C again to cancel the run, d to detach")
		}
		renderer.Tick()
		return false, "", nil
	}
	for {
		select {
		case snapshot, ok := <-snapshots:
			if !ok {
				// The observer stops unannounced only when its context ends.
				return ended()
			}
			if snapshot.err != nil {
				if ctx.Err() != nil {
					return attachOutcome{Status: "interrupted"}, nil
				}
				return attachOutcome{}, snapshot.err
			}
			shown = snapshot
			tree := snapshot.tree
			if tree.Run.Kind != "" {
				kind = tree.Run.Kind
			}
			if !runSettled(tree) {
				renderer.Render(tree)
				continue
			}
			// The answer to a cancel request still in flight says whether
			// the cancelled run was a first deployment.
			if cancelReplies != nil {
				if err := takeCancelReply(<-cancelReplies); err != nil {
					return attachOutcome{}, err
				}
			}
			renderer.Finish(tree)
			if continues && tree.Run.Status == "cancelled" {
				fmt.Fprintln(out, style.Yellow("this was the environment's first deployment, so there is no earlier "+
					"revision to return to: its revision keeps rolling out until the next deployment"))
			}
			return attachOutcome{Status: tree.Run.Status, Failure: tree.Run.Failure}, nil
		case reply := <-cancelReplies:
			if err := takeCancelReply(reply); err != nil {
				return attachOutcome{}, err
			}
		case <-attachCtx.Done():
			return ended()
		case <-interrupts:
			if done, status, err := interrupt(); done {
				return attachOutcome{Status: status}, err
			}
		case key := <-keys:
			switch {
			case isDetachKey(key):
				return detach()
			case key == 0x03:
				// A terminal without ISIG delivers Ctrl-C as a key.
				if done, status, err := interrupt(); done {
					return attachOutcome{Status: status}, err
				}
			}
		case <-disarm:
			disarm = nil
			renderer.Footer = hint
			renderer.Tick()
		case <-spin.C:
			renderer.Tick()
		}
	}
}

// attachHint is the footer that tells the user the run no longer needs
// this terminal and which keys act on it.
func attachHint(mode attachMode, style *clirender.Style) string {
	if mode == attachSessionEnds {
		return style.Dim("d detaches, Ctrl-C ends the session")
	}
	return style.Dim("the run continues on the server: d detaches, Ctrl-C cancels")
}

// runSettled reports whether the run reached a final status.
func runSettled(tree *client.RunTree) bool {
	switch tree.Run.Status {
	case "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

// runSnapshot is one observation of a run: its tree and the log tails the
// tree's frame shows, or the error that ended the observation.
type runSnapshot struct {
	tree  *client.RunTree
	tails map[string][]string
	err   error
}

// observeRun polls the run on its own goroutine until the run settles, a
// request fails, or ctx ends, and closes the channel when it stops. At
// most one poll is in flight, and the channel holds only the newest
// snapshot: a view that falls behind skips to the latest state, and the
// last snapshot, final or failed, is never dropped.
func observeRun(ctx context.Context, api *client.Client, runID string, style *clirender.Style, verbose bool) <-chan runSnapshot {
	snapshots := make(chan runSnapshot, 1)
	tails := &stepLogTails{api: api, style: style, verbose: verbose}
	go func() {
		defer close(snapshots)
		poll := time.NewTicker(attachPollInterval)
		defer poll.Stop()
		for {
			tree, err := api.GetRun(ctx, runID)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				publishLatest(snapshots, runSnapshot{err: err})
				return
			}
			lines := tails.refresh(ctx, tree)
			if ctx.Err() != nil {
				return
			}
			publishLatest(snapshots, runSnapshot{tree: tree, tails: lines})
			if runSettled(tree) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-poll.C:
			}
		}
	}()
	return snapshots
}

// publishLatest replaces the snapshot the view has not taken yet, if any.
// The observer is the channel's only sender, so the send never blocks.
func publishLatest(snapshots chan runSnapshot, snapshot runSnapshot) {
	select {
	case <-snapshots:
	default:
	}
	snapshots <- snapshot
}

// stepLogTails fetches the log tail shown under each live step, once per
// server poll and for all tail-worthy steps at once. It belongs to the
// observer: every refresh builds a fresh map and never writes a published
// one, so the view reads its snapshot's tails without a lock.
type stepLogTails struct {
	api     *client.Client
	style   *clirender.Style
	verbose bool
	lines   map[string][]string
}

// refresh fetches the tails the next frame of tree will show. A failed
// fetch keeps the step's previous tail rather than blanking it.
func (t *stepLogTails) refresh(ctx context.Context, tree *client.RunTree) map[string][]string {
	ids := clirender.TailStepIDs(tree)
	fresh := make(map[string][]string, len(ids))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			lines, ok := t.fetch(ctx, id)
			if !ok {
				lines = t.lines[id]
			}
			mu.Lock()
			fresh[id] = lines
			mu.Unlock()
		})
	}
	wg.Wait()
	t.lines = fresh
	return fresh
}

func (t *stepLogTails) fetch(ctx context.Context, stepID string) ([]string, bool) {
	logs, cursor, err := t.api.StepLogs(ctx, stepID, "", 0)
	if err != nil {
		return nil, false
	}
	if len(logs) == 0 {
		return nil, true
	}
	last := logs[len(logs)-1]
	switch {
	case last.Fields["tls"] == true:
		// A TLS checkpoint is a series of complete snapshots. Reach its
		// latest page, then show the attempt history compactly, or the
		// current snapshot's every field in verbose mode. The full history
		// remains in run logs.
		for len(logs) == 500 && cursor != "" {
			page, next, err := t.api.StepLogs(ctx, stepID, cursor, 0)
			if err != nil || len(page) == 0 {
				break
			}
			logs, cursor = page, next
		}
		if t.verbose {
			return clirender.CertificateDetailLines(logs[len(logs)-1], time.Now()), true
		}
		return clirender.CertificateLines(logs, time.Now(), t.style), true
	case last.Fields["health"] == true && !t.verbose:
		// Health snapshots supersede each other: only the latest matters.
		return clirender.HealthLines(last, t.style), true
	}
	tail := logs
	if len(tail) > 3 {
		tail = tail[len(tail)-3:]
	}
	lines := make([]string, len(tail))
	for index, entry := range tail {
		lines[index] = entry.Message
	}
	return lines, true
}
