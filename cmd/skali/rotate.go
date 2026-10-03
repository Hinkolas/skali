package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/compiler"
)

// The overlap window of a credential rotation: how long the previous
// credentials (a bucket's keypair, a database's login role) stay
// accepted. The API enforces the same bounds.
const (
	defaultRetireAfter = time.Hour
	minRetireAfter     = time.Minute
	maxRetireAfter     = 7 * 24 * time.Hour
)

// parseRetireAfter reads an overlap window such as 30m, 1h, or 2d.
func parseRetireAfter(text string) (time.Duration, error) {
	milliseconds, err := compiler.ParseDuration(strings.TrimSpace(text))
	if err != nil {
		return 0, err
	}
	if milliseconds <= 0 || milliseconds%1000 != 0 {
		return 0, errors.New("must be a positive duration in whole seconds such as 30m, 1h, or 2d")
	}
	window := time.Duration(milliseconds) * time.Millisecond
	if window < minRetireAfter || window > maxRetireAfter {
		return 0, fmt.Errorf("must be between %s and %s", describeWindow(minRetireAfter), describeWindow(maxRetireAfter))
	}
	return window, nil
}

// describeWindow words a window in its largest exact unit: 7d, 36h, 90m.
func describeWindow(window time.Duration) string {
	return shortRetention(int64(window / time.Second))
}
