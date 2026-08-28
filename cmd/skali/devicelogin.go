package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/browser"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
)

// Browser device authorization from the terminal: the CLI opens a request,
// shows the code, opens the console, and polls until the person approves.
// Used for login (skali remote add/login) and for confirming sudo mode on
// an aged session, so nobody types a password into the terminal on a
// machine with a browser. Non-interactive runs never come here; they keep
// the typed prompts (see remote.go and reauth.go).

// browserAuthAvailable reports whether the browser flow should be tried:
// an interactive terminal and no opt-out (--no-browser or SKALI_NO_BROWSER).
func browserAuthAvailable(noBrowser bool) bool {
	return cliprompt.Interactive() && !noBrowser && os.Getenv("SKALI_NO_BROWSER") == ""
}

// deviceLabel is what the approver sees: which machine is asking.
func deviceLabel() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "skali CLI"
	}
	return "skali CLI on " + host
}

// verificationURL is the console page for a code. The console is served
// at the master's root and the API under /api (single surface), so the
// page is the master URL without its API suffix.
func verificationURL(master, userCode string) string {
	root := strings.TrimSuffix(strings.TrimRight(master, "/"), "/api")
	return root + "/auth/device?code=" + userCode
}

// devicePollMinimum floors the poll cadence whatever the server says;
// tests shrink it. openBrowser is the opener, stubbed in tests.
var (
	devicePollMinimum = 5 * time.Second
	openBrowser       = browser.Open
)

// deviceStart opens a request; devicePoll polls it.
type (
	deviceStart func(ctx context.Context) (*client.DeviceRequest, error)
	devicePoll  func(ctx context.Context, deviceCode string) (*client.DevicePoll, error)
)

// runDeviceFlow drives one authorization: print the URL and code, open the
// browser, poll at the server's interval until the request settles or the
// person hits Ctrl-C. Only an approved answer returns without error.
func runDeviceFlow(ctx context.Context, out io.Writer, master string, start deviceStart, poll devicePoll) (*client.DevicePoll, error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	req, err := start(ctx)
	if err != nil {
		return nil, err
	}
	url := verificationURL(master, req.UserCode)
	style := clirender.StyleFor(out)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Confirm in your browser. If it did not open, visit:")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s\n", style.Link(url))
	fmt.Fprintf(out, "  Code: %s\n", style.Bold(req.UserCode))
	fmt.Fprintln(out)
	if err := openBrowser(url); err != nil {
		fmt.Fprintln(out, style.Dim("Could not open a browser automatically; open the URL above."))
	}
	fmt.Fprintln(out, style.Dim("Waiting for approval (Ctrl-C to cancel)..."))

	interval := time.Duration(req.Interval) * time.Second
	if interval < devicePollMinimum {
		interval = devicePollMinimum
	}
	deadline := req.ExpiresAt
	for {
		select {
		case <-ctx.Done():
			return nil, errors.New("cancelled; the browser request was not approved")
		case <-time.After(interval):
		}
		if !deadline.IsZero() && time.Now().After(deadline.Add(interval)) {
			return nil, errors.New("the code expired before it was approved; run the command again")
		}
		res, err := poll(ctx, req.DeviceCode)
		if client.IsSlowDown(err) {
			interval += devicePollMinimum
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, errors.New("cancelled; the browser request was not approved")
			}
			return nil, err
		}
		switch res.Status {
		case client.DevicePending:
			continue
		case client.DeviceApproved:
			return res, nil
		case client.DeviceDenied:
			return nil, errors.New("the request was denied in the browser")
		case client.DeviceExpired:
			return nil, errors.New("the code expired before it was approved; run the command again")
		default:
			return nil, fmt.Errorf("unexpected device authorization state %q", res.Status)
		}
	}
}

// deviceLogin logs in through the browser and returns the session plus
// the installation identity the master answered with, like loginSession.
func deviceLogin(ctx context.Context, out io.Writer, master string) (*client.SessionCreated, string, error) {
	c := client.New(master, "", userAgent())
	label := deviceLabel()
	res, err := runDeviceFlow(ctx, out, master,
		func(ctx context.Context) (*client.DeviceRequest, error) { return c.StartDeviceLogin(ctx, label) },
		c.PollDevice)
	if err != nil {
		return nil, "", err
	}
	if res.Session == nil {
		return nil, "", errors.New("the server approved the login without a session")
	}
	return res.Session, c.ObservedInstance(), nil
}

// deviceReauth refreshes the client's session through the browser.
func deviceReauth(ctx context.Context, out io.Writer, api *client.Client) error {
	label := deviceLabel()
	_, err := runDeviceFlow(ctx, out, api.Master(),
		func(ctx context.Context) (*client.DeviceRequest, error) { return api.StartDeviceReauth(ctx, label) },
		api.PollDevice)
	return err
}
