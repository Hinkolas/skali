//go:build !darwin && !linux

package main

import (
	"context"
	"errors"
)

func startDetached(context.Context, string, []string) (int, error) {
	return 0, errors.New("running in the background is supported on macOS and Linux only; run without -d")
}

func signalDetachedReady() {}
