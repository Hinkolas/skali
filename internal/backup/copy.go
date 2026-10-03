package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/minio/minio-go/v7"
	"golang.org/x/sync/errgroup"
)

// copyLog is the narration a copy emits; *journal.StepLog in production.
type copyLog interface {
	Info(ctx context.Context, message string)
	Progress(ctx context.Context, current, total int64)
}

const (
	// defaultCopyConcurrency is how many objects a bucket copy keeps in
	// flight. The copy is latency-bound (one round trip per small object),
	// so the win is roughly linear until the target throttles or the uplink
	// saturates; 16 stays well inside what one S3 endpoint accepts.
	defaultCopyConcurrency = 16
	// defaultCopyAttempts is one try plus three retries per object.
	defaultCopyAttempts = 4
	// copyProgressEvery is how many copied objects pass between journal
	// progress writes.
	copyProgressEvery = 16
)

// copyOptions tunes copyObjects; the zero value takes the defaults.
type copyOptions struct {
	// SkipMissing tolerates objects deleted from a live source during a
	// backup. Snapshot sources used by restores must remain complete.
	SkipMissing bool
	// Concurrency bounds the objects in flight at once.
	Concurrency int
	// Attempts is how often one object is tried before its error fails the
	// copy, counting the first try.
	Attempts int
	// Backoff returns how long to wait before retry number attempt (1 for
	// the first retry). nil takes exponential backoff with jitter.
	Backoff func(attempt int) time.Duration
}

func (o copyOptions) withDefaults() copyOptions {
	if o.Concurrency <= 0 {
		o.Concurrency = defaultCopyConcurrency
	}
	if o.Attempts <= 0 {
		o.Attempts = defaultCopyAttempts
	}
	if o.Backoff == nil {
		o.Backoff = defaultCopyBackoff
	}
	return o
}

// defaultCopyBackoff waits 500ms, 1s, 2s, ... capped at 8s, with up to 25%
// jitter so retries from parallel workers do not line up.
func defaultCopyBackoff(attempt int) time.Duration {
	delay := min(8*time.Second, 500*time.Millisecond<<min(attempt-1, 4))
	return delay + time.Duration(mathrand.Int64N(int64(delay/4)+1))
}

// copyObjects streams every object under sourcePrefix in source to
// destinationPrefix in destination, metadata included, reporting progress
// against expected (the listing's count when known, otherwise zero) and
// returning what it copied. It runs in-process: skalid has pod-network
// reach to both stores and streaming Get to Put keeps memory flat.
//
// One goroutine walks the listing; opts.Concurrency workers each copy one
// object at a time, because the copy is bound by round trips to the
// target, not by bandwidth. Transient failures are retried per object; the
// first permanent error cancels the remaining work and is returned. An
// object the listing named but the source no longer holds is skipped and
// noted only with SkipMissing: the loose consistency of a live backup.
func copyObjects(ctx context.Context, log copyLog, source, destination objectStore,
	sourcePrefix, destinationPrefix string, expected int64, opts copyOptions) (count, bytes int64, err error) {
	opts = opts.withDefaults()
	var (
		copied, copiedBytes, skipped atomic.Int64
		progress                     = progressReporter{log: log, total: expected}
	)
	group, workerCtx := errgroup.WithContext(ctx)
	objects := make(chan objectInfo)
	group.Go(func() error {
		defer close(objects)
		return source.List(workerCtx, sourcePrefix, func(info objectInfo) error {
			select {
			case objects <- info:
				return nil
			case <-workerCtx.Done():
				return workerCtx.Err()
			}
		})
	})
	for range opts.Concurrency {
		group.Go(func() error {
			for info := range objects {
				key := destinationPrefix + info.Key[len(sourcePrefix):]
				switch err := copyOne(workerCtx, source, destination, info.Key, key, info.Size, opts); {
				case opts.SkipMissing && errors.Is(err, errNotFound):
					skipped.Add(1)
				case err != nil:
					return fmt.Errorf("copy %s: %w", info.Key, err)
				default:
					copiedBytes.Add(info.Size)
					progress.report(workerCtx, copied.Add(1))
				}
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return copied.Load(), copiedBytes.Load(), err
	}
	count, bytes = copied.Load(), copiedBytes.Load()
	if n := skipped.Load(); n > 0 {
		log.Info(ctx, fmt.Sprintf("skipped %d objects that were deleted during the copy", n))
	}
	if expected > 0 && count != expected {
		// The bucket drifted during the copy: the documented loose
		// consistency of a live snapshot, worth a line in the log.
		log.Info(ctx, fmt.Sprintf("listing changed during the copy: expected %d objects, copied %d", expected, count))
	}
	return count, bytes, nil
}

// copyOne moves one object, retrying the whole Get→Put on transient
// failures. The retry has to sit here: minio-go only retries requests
// whose body it can rewind, and a streamed Get is not one.
func copyOne(ctx context.Context, source, destination objectStore, sourceKey, destinationKey string,
	size int64, opts copyOptions) error {
	for attempt := 1; ; attempt++ {
		err := copyOnce(ctx, source, destination, sourceKey, destinationKey, size)
		if err == nil || errors.Is(err, errNotFound) {
			return err
		}
		if attempt >= opts.Attempts || !retryableCopyError(err) {
			if attempt > 1 {
				return fmt.Errorf("copy %s failed after %d attempts: %w", sourceKey, attempt, err)
			}
			return err
		}
		timer := time.NewTimer(opts.Backoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("copy %s interrupted: %w", sourceKey, ctx.Err())
		case <-timer.C:
		}
	}
}

func copyOnce(ctx context.Context, source, destination objectStore, sourceKey, destinationKey string, size int64) error {
	reader, meta, err := source.GetWithMeta(ctx, sourceKey)
	if err != nil {
		return err
	}
	defer reader.Close()
	return destination.PutWithMeta(ctx, destinationKey, reader, size, meta)
}

// retryableCopyError tells a blip (the target or the gateway answered with
// a server error or throttling, or the connection failed) from a verdict
// (denied, invalid, missing, cancelled). Unknown errors are verdicts: a
// retry that cannot help only delays the failure the operator must see.
func retryableCopyError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, errNotFound) {
		return false
	}
	var response minio.ErrorResponse
	if errors.As(err, &response) {
		switch response.StatusCode {
		case 408, 429, 499, 500, 502, 503, 504, 520:
			return true
		}
		switch response.Code {
		case "RequestTimeout", "InternalError", "ServiceUnavailable",
			"SlowDown", "SlowDownRead", "SlowDownWrite",
			"Throttling", "ThrottlingException", "RequestThrottled", "RequestLimitExceeded":
			return true
		}
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return minio.IsNetworkOrHostDown(err, false)
}

// progressReporter writes the journal's progress every copyProgressEvery
// objects and on the last one, never backwards: with parallel workers a
// late report for a smaller count could otherwise overwrite a larger one.
type progressReporter struct {
	log   copyLog
	total int64
	mu    sync.Mutex
	last  int64
}

func (p *progressReporter) report(ctx context.Context, current int64) {
	if current%copyProgressEvery != 0 && current != p.total {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if current <= p.last {
		return
	}
	p.last = current
	p.log.Progress(ctx, current, p.total)
}
