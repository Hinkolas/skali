// Package truststore installs the local platform's development CA into the
// trust stores browsers on this machine consult, and reports whether it is
// there: the login keychain on macOS, the system anchors plus the NSS
// databases of Chrome and Firefox on Linux. The CLI itself never needs it
// (it carries the CA in its own clients); browsers do.
//
// Every store is checked before it is written, and a write is only ever an
// addition of one certificate, so the package is safe to run repeatedly.
// Platform selection happens at run time so the whole package stays
// testable on any OS.
package truststore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Certificate is the CA to trust: the PEM file on disk (what the platform
// tools import), its bytes (for byte-exact presence checks), and the name a
// store lists it under.
type Certificate struct {
	Path string
	PEM  []byte
	Name string
}

// State is one store's verdict.
type State string

const (
	// StateTrusted: the store holds this exact CA as a trusted root.
	StateTrusted State = "trusted"
	// StateMissing: the store exists and does not trust the CA.
	StateMissing State = "missing"
	// StateUnavailable: the store cannot be reached from here (a tool is
	// not installed, or the OS is unsupported); Detail says what to do.
	StateUnavailable State = "unavailable"
	// StateFailed: an install into the store was attempted and failed;
	// Detail carries the tool's answer.
	StateFailed State = "failed"
)

// StoreStatus is one trust store's verdict.
type StoreStatus struct {
	Name   string
	State  State
	Detail string
}

// Status is the verdict across every store this machine has.
type Status struct {
	Stores []StoreStatus
}

// Trusted reports whether every reachable store trusts the CA. Stores that
// cannot be reached (a missing tool) do not count against it, since nothing
// can be done about them here, but a machine with no reachable store at
// all is not trusted.
func (s Status) Trusted() bool {
	reachable := 0
	for _, store := range s.Stores {
		switch store.State {
		case StateUnavailable:
			continue
		case StateTrusted:
			reachable++
		default:
			return false
		}
	}
	return reachable > 0
}

// store is one trust store's implementation.
type store interface {
	name() string
	// check reports StateTrusted, StateMissing, or StateUnavailable with
	// detail; an error is an unexpected tool failure.
	check(ctx context.Context, certificate Certificate) (State, string, error)
	// install adds the CA; it is only called after check said missing.
	install(ctx context.Context, certificate Certificate) error
}

// The seams tests replace: the process runner, PATH lookups, the file
// system root the Linux anchor directories are probed under, the home
// directory, and the OS.
var (
	run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	lookPath = exec.LookPath
	fsRoot   = "/"
	homeDir  = os.UserHomeDir
	goos     = runtime.GOOS
	isRoot   = func() bool { return os.Geteuid() == 0 }
)

// stores enumerates this machine's trust stores.
func stores() []store {
	switch goos {
	case "darwin":
		return []store{darwinKeychain{}}
	case "linux":
		return linuxStores()
	default:
		return []store{unsupported{os: goos}}
	}
}

// Check reports whether the CA is trusted, store by store.
func Check(ctx context.Context, certificate Certificate) (Status, error) {
	var status Status
	for _, s := range stores() {
		state, detail, err := s.check(ctx, certificate)
		if err != nil {
			return status, fmt.Errorf("truststore: check %s: %w", s.name(), err)
		}
		status.Stores = append(status.Stores, StoreStatus{Name: s.name(), State: state, Detail: detail})
	}
	return status, nil
}

// Install adds the CA to every reachable store that does not trust it yet
// and reports the resulting state. A store that refuses (a cancelled
// password dialog, a missing sudo) reads as failed with the tool's answer;
// the other stores are still attempted.
func Install(ctx context.Context, certificate Certificate) (Status, error) {
	var status Status
	for _, s := range stores() {
		state, detail, err := s.check(ctx, certificate)
		if err != nil {
			return status, fmt.Errorf("truststore: check %s: %w", s.name(), err)
		}
		if state == StateMissing {
			if err := s.install(ctx, certificate); err != nil {
				state, detail = StateFailed, err.Error()
			} else {
				state, detail = StateTrusted, "installed now"
			}
		}
		status.Stores = append(status.Stores, StoreStatus{Name: s.name(), State: state, Detail: detail})
	}
	return status, nil
}

// unsupported is the single pseudo-store of an OS this package does not
// know how to write to.
type unsupported struct{ os string }

func (u unsupported) name() string { return u.os + " trust store" }

func (u unsupported) check(context.Context, Certificate) (State, string, error) {
	return StateUnavailable, "import the CA certificate into your browser or OS trust store by hand", nil
}

func (u unsupported) install(context.Context, Certificate) error {
	return errors.New("no automatic trust installation on " + u.os)
}

// toolError renders a failed tool invocation with its output on one line.
func toolError(name string, out []byte, err error) error {
	trimmed := strings.TrimSpace(string(bytes.ReplaceAll(out, []byte("\n"), []byte(" "))))
	if trimmed == "" {
		return fmt.Errorf("%s: %w", name, err)
	}
	return fmt.Errorf("%s: %s", name, trimmed)
}

// OS reports the operating system the stores are selected for.
func OS() string { return goos }
