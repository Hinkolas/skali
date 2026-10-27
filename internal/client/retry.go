package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"syscall"
	"time"
)

// readRetry paces the retries of an idempotent read: the delay starts at
// first and doubles up to max, and no retry starts that would end more than
// window after the first attempt. The window covers a skalid pod restart
// (scheduling, migrations, the readiness probe); an outage that outlasts
// it ends the command with the last failure.
type readRetry struct {
	first  time.Duration
	max    time.Duration
	window time.Duration
}

var defaultReadRetry = readRetry{first: 250 * time.Millisecond, max: 4 * time.Second, window: 20 * time.Second}

// read sends a GET and repeats it with bounded backoff while it fails
// transiently, so a run tree, status, or log poll survives a connection
// reset or the edge's 502/503 while skalid restarts. Any other failure
// (authentication, authorization, validation, a changed installation, a
// slow server) returns at once. Cancelling ctx ends the wait immediately,
// and no wait runs past ctx's deadline: the read gives up early with the
// last failure instead.
func (c *Client) read(ctx context.Context, path string, out any, limit int64) error {
	started := time.Now()
	delay := c.reads.first
	for attempt := 1; ; attempt++ {
		transient, err := c.send(ctx, http.MethodGet, path, nil, out, limit)
		if err == nil || !transient {
			return err
		}
		wait := delay + time.Duration(mathrand.Int64N(int64(delay/4)+1))
		elapsed := time.Since(started)
		deadline, bounded := ctx.Deadline()
		if elapsed+wait > c.reads.window || bounded && time.Until(deadline) < wait {
			if attempt == 1 {
				return err
			}
			return &retriesExhaustedError{master: c.base, attempts: attempt, elapsed: elapsed, err: err}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("client: %s: %w", c.base, ctx.Err())
		case <-timer.C:
		}
		delay = min(2*delay, c.reads.max)
	}
}

// transientTransport reports whether a failed exchange is worth repeating:
// the connection broke (reset, or closed before the response was complete)
// or, once the master has answered this client before, nothing listened
// for a moment because skalid or its edge restarts. A refusal on first
// contact means nothing serves the address at all and fails fast, as do
// timeouts, DNS, and TLS failures.
func (c *Client) transientTransport(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	switch {
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case errors.Is(err, syscall.ECONNREFUSED):
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.answered
	}
	return false
}

// edgeUnavailable reports a gateway failure the edge answered on skalid's
// behalf: a 502, 503, or 504 without skalid's error envelope. skalid's own
// 503s carry a code (no backup target, registry disabled) and are final.
func edgeUnavailable(status int, raw []byte) bool {
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return false
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(raw, &envelope) != nil || envelope.Error.Code == ""
}

// retriesExhaustedError is a read that failed transiently on every attempt
// of its window. It unwraps to the last failure, so callers still branch
// on an *APIError's status.
type retriesExhaustedError struct {
	master   string
	attempts int
	elapsed  time.Duration
	err      error
}

func (e *retriesExhaustedError) Error() string {
	cause := e.err.Error()
	var apiErr *APIError
	if errors.As(e.err, &apiErr) {
		cause = fmt.Sprintf("client: %s answered HTTP %d %s", e.master, apiErr.Status, http.StatusText(apiErr.Status))
	}
	return fmt.Sprintf("%s; still failing after %d attempts over %s", cause, e.attempts, e.elapsed.Round(100*time.Millisecond))
}

func (e *retriesExhaustedError) Unwrap() error { return e.err }
