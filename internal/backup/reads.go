package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// The waits that watch an operation's work (its Jobs, the environment's
// workloads stopping, the environment converging after a restore) read
// the cluster or the store over and over. One failed read says nothing
// about the work: the API server restarting, a dropped connection or
// throttling would otherwise fail a backup whose Job is fine, or a
// restore halfway through. A wait instead backs off and reads again, and
// gives up only when reads kept failing for readFailureGrace, or at once
// on an answer that will not change (not found, denied, invalid).
var (
	readFailureGrace = 2 * time.Minute
	readBackoffBase  = 2 * time.Second
	readBackoffMax   = 15 * time.Second
)

// readFailures tracks one wait's current run of failed reads.
type readFailures struct {
	since time.Time
	count int
}

// retry returns how long to wait before reading again after err, or the
// error that ends the wait: the wait's own cancellation, an answer that
// will not change, or failures that lasted readFailureGrace.
func (r *readFailures) retry(ctx context.Context, err error) (time.Duration, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if !transientRead(err) {
		return 0, err
	}
	now := time.Now()
	if r.count == 0 {
		r.since = now
	}
	r.count++
	if failing := now.Sub(r.since); failing >= readFailureGrace {
		return 0, fmt.Errorf("%w (reads failed for %s)", err, failing.Round(time.Second))
	}
	return min(readBackoffMax, readBackoffBase<<min(r.count-1, 4)), nil
}

// ok ends the run of failures.
func (r *readFailures) ok() {
	r.count = 0
}

// transientRead tells a read that may answer next time from one whose
// answer stands. Unknown errors count as transient: the grace bounds
// them.
func transientRead(err error) bool {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, pgx.ErrNoRows),
		apierrors.IsNotFound(err), apierrors.IsGone(err),
		apierrors.IsForbidden(err), apierrors.IsUnauthorized(err),
		apierrors.IsBadRequest(err), apierrors.IsInvalid(err), apierrors.IsMethodNotSupported(err):
		return false
	}
	return true
}

// pause waits d or until ctx ends.
func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
