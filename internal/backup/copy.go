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
	// defaultCopyStallTimeout is how long one object's copy may go without
	// moving a byte, in either direction, before the attempt is abandoned
	// as stalled. It sits above the S3 client's one-minute wait for
	// response headers, which bounds the requests themselves; nothing
	// bounds a response body that stops arriving or a request body the
	// other side stops taking, except this.
	defaultCopyStallTimeout = 2 * time.Minute
	// defaultCopyTimeout bounds one bucket component as a whole. It is a
	// backstop above the stall bound, which ends a copy that stopped
	// moving: a copy that keeps moving slowly ends here.
	defaultCopyTimeout = 6 * time.Hour
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
	// StallTimeout abandons an attempt that moved no bytes for this long;
	// the stall counts as a transient failure and is retried.
	StallTimeout time.Duration
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
	if o.StallTimeout <= 0 {
		o.StallTimeout = defaultCopyStallTimeout
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
// target, not by bandwidth. An attempt that stops moving bytes is
// abandoned after opts.StallTimeout. Transient failures, stalls included,
// are retried per object; the first permanent error cancels the remaining
// work and is returned. An object the listing named but the source no
// longer holds is skipped and noted only with SkipMissing: the loose
// consistency of a live backup.
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
		err := copyOnce(ctx, source, destination, sourceKey, destinationKey, size, opts.StallTimeout)
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

// copyOnce is one attempt: a Get streamed into a Put under a watch that
// abandons the attempt once no byte moved for stall. Bytes read from the
// source and bytes the upload sent both count, so neither a source that
// stops answering nor a destination that stops taking data holds it.
func copyOnce(ctx context.Context, source, destination objectStore, sourceKey, destinationKey string,
	size int64, stall time.Duration) error {
	ctx, watch := watchProgress(ctx, stall)
	defer watch.stop()
	reader, meta, err := source.GetWithMeta(ctx, sourceKey)
	if err == nil {
		defer reader.Close()
		err = destination.PutWithMeta(ctx, destinationKey, watch.reader(reader), size, meta)
	}
	if err != nil {
		if stalled := watch.stalled(); stalled != nil {
			return stalled
		}
	}
	return err
}

// copyStalledError is an attempt abandoned because no byte moved for
// Window. It is transient: the next attempt opens new connections.
type copyStalledError struct {
	Window time.Duration
}

func (e *copyStalledError) Error() string {
	return fmt.Sprintf("no data moved for %s", e.Window)
}

// progressWatch cancels one attempt's context once it went window without
// progress. Reads through reader and the upload's progress hook (Read on
// the watch itself, which minio-go calls with the bytes it sent) are the
// progress.
type progressWatch struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	window time.Duration
	last   atomic.Int64
	timer  *time.Timer

	mu      sync.Mutex
	stopped bool
}

func watchProgress(ctx context.Context, window time.Duration) (context.Context, *progressWatch) {
	ctx, cancel := context.WithCancelCause(ctx)
	w := &progressWatch{cancel: cancel, window: window}
	w.ctx = withUploadProgress(ctx, w)
	w.touch()
	w.timer = time.AfterFunc(window, w.check)
	return w.ctx, w
}

func (w *progressWatch) touch() {
	w.last.Store(time.Now().UnixNano())
}

func (w *progressWatch) check() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	idle := time.Since(time.Unix(0, w.last.Load()))
	if idle >= w.window {
		w.cancel(&copyStalledError{Window: w.window})
		return
	}
	w.timer.Reset(w.window - idle)
}

// stalled returns the stall that ended the attempt, if one did; a cancel
// from outside is not a stall.
func (w *progressWatch) stalled() error {
	var stalled *copyStalledError
	if errors.As(context.Cause(w.ctx), &stalled) {
		return stalled
	}
	return nil
}

func (w *progressWatch) stop() {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	w.timer.Stop()
	w.cancel(nil)
}

// Read is the upload's progress hook: it takes the bytes sent.
func (w *progressWatch) Read(p []byte) (int, error) {
	if len(p) > 0 {
		w.touch()
	}
	return len(p), nil
}

// reader counts the bytes read from r as progress.
func (w *progressWatch) reader(r io.Reader) io.Reader {
	return watchedReader{r: r, watch: w}
}

type watchedReader struct {
	r     io.Reader
	watch *progressWatch
}

func (r watchedReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.watch.touch()
	}
	return n, err
}

// uploadProgressKey carries an upload's progress hook to the store that
// performs it, the way httptrace carries a client trace: the hook belongs
// to the attempt, not to the object store's interface.
type uploadProgressKey struct{}

func withUploadProgress(ctx context.Context, hook io.Reader) context.Context {
	return context.WithValue(ctx, uploadProgressKey{}, hook)
}

// uploadProgress returns the context's upload progress hook, or nil.
func uploadProgress(ctx context.Context) io.Reader {
	hook, _ := ctx.Value(uploadProgressKey{}).(io.Reader)
	return hook
}

// retryableCopyError tells a blip (the target or the gateway answered with
// a server error or throttling, or the connection failed) from a verdict
// (denied, invalid, missing, cancelled). Unknown errors are verdicts: a
// retry that cannot help only delays the failure the operator must see.
func retryableCopyError(err error) bool {
	var stalled *copyStalledError
	if errors.As(err, &stalled) {
		return true
	}
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
