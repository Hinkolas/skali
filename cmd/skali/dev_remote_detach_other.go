//go:build !darwin && !linux

package main

import (
	"context"
	"errors"
	"net"

	"github.com/Hinkolas/skali/internal/devremote"
)

func startDetached(context.Context, string, []string, []net.Listener) (int, error) {
	return 0, errors.New("running in the background is supported on macOS and Linux only; run without -d")
}

func signalDetachedReady() {}

func inheritedListeners([]devremote.Route) ([]net.Listener, bool, error) { return nil, false, nil }
