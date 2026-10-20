//go:build !darwin && !linux

package devremote

import (
	"context"
	"errors"
	"net"
)

var errNoListenHelper = errors.New("the listen helper runs on macOS and Linux only")

func ListenThrough(context.Context, []Route, func(context.Context, []string) error) ([]net.Listener, error) {
	return nil, &HandoffError{Err: errNoListenHelper}
}

func ListenHelper(string, []string) error { return errNoListenHelper }
