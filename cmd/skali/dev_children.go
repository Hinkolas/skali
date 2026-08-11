package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/utils"
)

// childGrace is how long a terminating dev child gets between SIGTERM and
// SIGKILL.
const childGrace = 10 * time.Second

// devChildren are the dev-block applications running on the host for this
// session. They never outlive the CLI: detach and the session epilogue
// terminate them before anything else happens.
type devChildren struct {
	children []*devChild
}

type devChild struct {
	key  string
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// startDevChildren resolves each application's environment through the
// local platform (sudo-gated like credential reveal; reauthLocal covers the
// window non-interactively) and starts its dev command with stdout and
// stderr multiplexed. Called after the deploy settled so the just-staged
// values and revision are active. A crashed child is reported through the
// mux and the session keeps running; nothing restarts automatically.
func startDevChildren(ctx context.Context, mux *logMux, out io.Writer, api *client.Client,
	environmentID, root string, apps map[string]manifest.Dev) (*devChildren, error) {
	style := clirender.StyleFor(out)
	group := &devChildren{}
	for _, key := range utils.SortedKeys(apps) {
		dev := apps[key]
		resolved, err := api.ApplicationEnvironment(ctx, environmentID, key, localdev.LoopbackPortBase())
		if isReauthRequired(err) {
			if err = reauthLocal(ctx, api); err == nil {
				resolved, err = api.ApplicationEnvironment(ctx, environmentID, key, localdev.LoopbackPortBase())
			}
		}
		if err != nil {
			group.Terminate(childGrace)
			return nil, fmt.Errorf("resolve environment for %s: %w", key, err)
		}
		for _, warning := range resolved.Warnings {
			fmt.Fprintf(out, "  %s\n", style.Yellow("warning: "+key+": "+warning))
		}
		child, err := startDevChild(ctx, mux, key, dev, root, resolved.Values)
		if err != nil {
			group.Terminate(childGrace)
			return nil, err
		}
		group.children = append(group.children, child)
		fmt.Fprintf(out, "  %s      %s\n", style.Dim("local"), key+": "+strings.Join(dev.Command, " "))
		go watchDevChild(ctx, mux, child)
	}
	return group, nil
}

func startDevChild(ctx context.Context, mux *logMux, key string, dev manifest.Dev,
	root string, env map[string]string) (*devChild, error) {
	// CommandContext alone would SIGKILL on context end; configureChildProcess
	// replaces that with a process-group SIGTERM and a grace period, so the
	// dev server (and its grandchildren, e.g. a shell-wrapped node) can shut
	// down cleanly.
	cmd := exec.CommandContext(ctx, dev.Command[0], dev.Command[1:]...)
	cmd.Dir = root
	environ := os.Environ()
	for _, name := range utils.SortedKeys(env) {
		environ = append(environ, name+"="+env[name])
	}
	cmd.Env = environ
	configureChildProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("dev command for %s: %w", key, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("dev command for %s: %w", key, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start dev command for %s: %w", key, err)
	}
	child := &devChild{key: key, cmd: cmd, done: make(chan struct{})}
	writer := mux.Writer(key + " | ")
	var wg sync.WaitGroup
	wg.Add(2)
	pipe := func(source io.Reader) {
		defer wg.Done()
		_, _ = io.Copy(writer, source)
	}
	go pipe(stdout)
	go pipe(stderr)
	go func() {
		wg.Wait()
		writer.Flush()
		child.err = cmd.Wait()
		close(child.done)
	}()
	return child, nil
}

// watchDevChild reports a child that exited while the session lives; the
// rest of the session (cluster logs, other children) keeps running, exactly
// like a crashing pod would not end the follow.
func watchDevChild(ctx context.Context, mux *logMux, child *devChild) {
	select {
	case <-ctx.Done():
	case <-child.done:
		if ctx.Err() != nil {
			return
		}
		detail := "exited"
		if child.err != nil {
			detail = "exited: " + child.err.Error()
		}
		mux.writeLine(child.key+" | ", "dev command "+detail+
			" (the session keeps running; fix it and restart skali dev)")
	}
}

// Terminate stops every child: SIGTERM to each process group, one shared
// grace period, then SIGKILL for stragglers. Safe on nil and safe to call
// more than once.
func (c *devChildren) Terminate(grace time.Duration) {
	if c == nil {
		return
	}
	for _, child := range c.children {
		terminateChild(child.cmd)
	}
	deadline := time.After(grace)
	for _, child := range c.children {
		select {
		case <-child.done:
		case <-deadline:
			killChild(child.cmd)
			<-child.done
		}
	}
}
