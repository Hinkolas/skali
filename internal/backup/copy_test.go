package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/require"
)

type quietLog struct {
	mu         sync.Mutex
	progress   []int64
	infos      []string
	infoErrors []error
}

func (l *quietLog) Info(ctx context.Context, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, message)
	l.infoErrors = append(l.infoErrors, ctx.Err())
}

func (l *quietLog) Progress(_ context.Context, current, _ int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.progress = append(l.progress, current)
}

func (l *quietLog) notes() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.infos...)
}

// gatedStore holds every PutWithMeta until the test releases it, counting
// how many are blocked at once: the probe for the concurrency bound.
type gatedStore struct {
	*memoryStore
	release     chan struct{}
	inflight    atomic.Int32
	maxInflight atomic.Int32
}

func newGatedStore() *gatedStore {
	return &gatedStore{memoryStore: newMemoryStore(), release: make(chan struct{})}
}

func (g *gatedStore) PutWithMeta(ctx context.Context, key string, r io.Reader, size int64, meta objectMeta) error {
	n := g.inflight.Add(1)
	defer g.inflight.Add(-1)
	for {
		seen := g.maxInflight.Load()
		if n <= seen || g.maxInflight.CompareAndSwap(seen, n) {
			break
		}
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return g.memoryStore.PutWithMeta(ctx, key, r, size, meta)
}

// flakyStore fails PutWithMeta for a key with each queued error in turn
// before letting it through, and counts the attempts per key.
type flakyStore struct {
	*memoryStore
	mu       sync.Mutex
	failures map[string][]error
	attempts map[string]int
}

func newFlakyStore(failures map[string][]error) *flakyStore {
	return &flakyStore{memoryStore: newMemoryStore(), failures: failures, attempts: map[string]int{}}
}

func (f *flakyStore) PutWithMeta(ctx context.Context, key string, r io.Reader, size int64, meta objectMeta) error {
	f.mu.Lock()
	f.attempts[key]++
	var err error
	if queue := f.failures[key]; len(queue) > 0 {
		err, f.failures[key] = queue[0], queue[1:]
	}
	f.mu.Unlock()
	if err != nil {
		return fmt.Errorf("backup: put %s: %w", key, err)
	}
	return f.memoryStore.PutWithMeta(ctx, key, r, size, meta)
}

func (f *flakyStore) attemptsFor(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts[key]
}

// vanishingStore lists one key its Get no longer serves: an object deleted
// between the listing and the copy.
type vanishingStore struct {
	*memoryStore
	gone string
}

func (v *vanishingStore) GetWithMeta(ctx context.Context, key string) (io.ReadCloser, objectMeta, error) {
	if key == v.gone {
		return nil, objectMeta{}, errNotFound
	}
	return v.memoryStore.GetWithMeta(ctx, key)
}

func fillStore(t *testing.T, store objectStore, prefix string, n int) {
	t.Helper()
	for i := range n {
		key := fmt.Sprintf("%sobject-%03d", prefix, i)
		require.NoError(t, store.Put(context.Background(), key, strings.NewReader(key), int64(len(key))))
	}
}

var noBackoff = func(int) time.Duration { return 0 }

// TestCopyObjectsPreservesMetadata: the copy path between buckets carries
// content headers, user metadata, and tags, rewrites the prefix, and
// leaves objects outside the source prefix alone.
func TestCopyObjectsPreservesMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source, destination := newMemoryStore(), newMemoryStore()
	expires := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	meta := objectMeta{
		ContentType:        "image/png",
		ContentEncoding:    "identity",
		ContentDisposition: `attachment; filename="photo.png"`,
		ContentLanguage:    "de",
		CacheControl:       "max-age=60",
		Expires:            expires,
		UserMetadata:       map[string]string{"Owner": "alice"},
		Tags:               map[string]string{"tier": "hot"},
	}
	require.NoError(t, source.PutWithMeta(ctx, "snap/1/photos/a.png", strings.NewReader("png-bytes"), 9, meta))
	require.NoError(t, source.Put(ctx, "snap/1/plain.txt", strings.NewReader("plain"), 5))
	require.NoError(t, source.Put(ctx, "snap/2/other.txt", strings.NewReader("other"), 5))

	log := &quietLog{}
	count, bytes, err := copyObjects(ctx, log, source, destination, "snap/1/", "", 2, copyOptions{})
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
	require.EqualValues(t, 14, bytes)
	require.Equal(t, []int64{2}, log.progress, "progress reports on the final object")
	require.Empty(t, log.notes(), "a copy that matches its listing has nothing to note")

	reader, got, err := destination.GetWithMeta(ctx, "photos/a.png")
	require.NoError(t, err)
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "png-bytes", string(data))
	require.Equal(t, meta, got)
	_, plain, err := destination.GetWithMeta(ctx, "plain.txt")
	require.NoError(t, err)
	require.Equal(t, objectMeta{}, plain)
	require.False(t, destination.has("other.txt"), "objects outside the prefix are not copied")

	// The reverse direction (restore) re-applies the prefix.
	back := newMemoryStore()
	count, _, err = copyObjects(ctx, log, destination, back, "", "restored/", 0, copyOptions{})
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
	_, got, err = back.GetWithMeta(ctx, "restored/photos/a.png")
	require.NoError(t, err)
	require.Equal(t, meta, got)
}

// TestCopyObjectsBoundedConcurrency: more than one object is in flight,
// never more than the bound, and everything lands once the target answers.
func TestCopyObjectsBoundedConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source, destination := newMemoryStore(), newGatedStore()
	fillStore(t, source, "", 40)

	log := &quietLog{}
	done := make(chan error, 1)
	var count int64
	go func() {
		var err error
		count, _, err = copyObjects(ctx, log, source, destination, "", "", 40, copyOptions{Concurrency: 4})
		done <- err
	}()

	require.Eventually(t, func() bool { return destination.inflight.Load() == 4 },
		5*time.Second, time.Millisecond, "the pool fills up to the bound")
	require.Never(t, func() bool { return destination.inflight.Load() > 4 },
		100*time.Millisecond, time.Millisecond, "the bound holds")
	close(destination.release)
	require.NoError(t, <-done)
	require.EqualValues(t, 40, count)
	require.EqualValues(t, 4, destination.maxInflight.Load())
	for i := range 40 {
		require.True(t, destination.has(fmt.Sprintf("object-%03d", i)))
	}
	// Progress never runs backwards and ends on the final count.
	last := int64(0)
	for _, current := range log.progress {
		require.Greater(t, current, last)
		last = current
	}
	require.EqualValues(t, 40, last)
}

// TestCopyObjectsNotesDrift: the listing-changed note and the skipped note
// stay correct when the bucket moved under the copy.
func TestCopyObjectsNotesDrift(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("ListingShrank", func(t *testing.T) {
		t.Parallel()
		source, destination := newMemoryStore(), newMemoryStore()
		fillStore(t, source, "", 20)
		log := &quietLog{}
		count, _, err := copyObjects(ctx, log, source, destination, "", "", 25, copyOptions{Concurrency: 4})
		require.NoError(t, err)
		require.EqualValues(t, 20, count)
		require.Equal(t, []string{"listing changed during the copy: expected 25 objects, copied 20"}, log.notes())
		for _, err := range log.infoErrors {
			require.NoError(t, err, "the journal must receive an active context after the workers finish")
		}
	})

	t.Run("ObjectVanished", func(t *testing.T) {
		t.Parallel()
		inner, destination := newMemoryStore(), newMemoryStore()
		fillStore(t, inner, "", 20)
		source := &vanishingStore{memoryStore: inner, gone: "object-007"}
		log := &quietLog{}
		count, bytes, err := copyObjects(ctx, log, source, destination, "", "", 20, copyOptions{Concurrency: 4, SkipMissing: true})
		require.NoError(t, err, "an object deleted during the copy is not a failure")
		require.EqualValues(t, 19, count)
		require.EqualValues(t, 19*len("object-000"), bytes)
		require.False(t, destination.has("object-007"))
		require.Equal(t, []string{
			"skipped 1 objects that were deleted during the copy",
			"listing changed during the copy: expected 20 objects, copied 19",
		}, log.notes())
	})
}

// A snapshot is not a live source: an object disappearing while restoring
// must fail rather than silently reporting a partial restore as successful.
func TestCopyObjectsMissingSnapshotObjectFails(t *testing.T) {
	t.Parallel()
	inner, destination := newMemoryStore(), newMemoryStore()
	fillStore(t, inner, "snapshot/", 2)
	source := &vanishingStore{memoryStore: inner, gone: "snapshot/object-000"}
	_, _, err := copyObjects(context.Background(), &quietLog{}, source, destination,
		"snapshot/", "", 2, copyOptions{})
	require.ErrorIs(t, err, errNotFound)
	require.ErrorContains(t, err, "snapshot/object-000")
}

// TestCopyObjectsRetriesTransientFailures: a throttled or failing target
// gets a few more tries per object; a verdict fails the copy at once with
// the key in the error.
func TestCopyObjectsRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("TransientSucceedsOnRetry", func(t *testing.T) {
		t.Parallel()
		source := newMemoryStore()
		fillStore(t, source, "", 8)
		destination := newFlakyStore(map[string][]error{
			"object-003": {
				minio.ErrorResponse{StatusCode: 503, Code: "SlowDown"},
				&net.OpError{Op: "read", Err: syscall.ECONNRESET},
			},
			"object-005": {minio.ErrorResponse{StatusCode: 500, Code: "InternalError"}},
		})
		log := &quietLog{}
		count, _, err := copyObjects(ctx, log, source, destination, "", "", 8,
			copyOptions{Concurrency: 2, Backoff: noBackoff})
		require.NoError(t, err)
		require.EqualValues(t, 8, count)
		require.Equal(t, 3, destination.attemptsFor("object-003"))
		require.Equal(t, 2, destination.attemptsFor("object-005"))
		require.Equal(t, 1, destination.attemptsFor("object-000"))
		require.True(t, destination.has("object-003"))
	})

	t.Run("TransientExhausted", func(t *testing.T) {
		t.Parallel()
		source := newMemoryStore()
		fillStore(t, source, "", 2)
		throttled := minio.ErrorResponse{StatusCode: 503, Code: "SlowDown"}
		destination := newFlakyStore(map[string][]error{
			"object-001": {throttled, throttled, throttled, throttled},
		})
		_, _, err := copyObjects(ctx, &quietLog{}, source, destination, "", "", 2,
			copyOptions{Concurrency: 1, Attempts: 3, Backoff: noBackoff})
		require.Error(t, err)
		require.Contains(t, err.Error(), "copy object-001 failed after 3 attempts")
		require.ErrorAs(t, err, &minio.ErrorResponse{})
		require.Equal(t, 3, destination.attemptsFor("object-001"))
	})

	t.Run("PermanentFailsFast", func(t *testing.T) {
		t.Parallel()
		source := newMemoryStore()
		fillStore(t, source, "", 40)
		denied := minio.ErrorResponse{StatusCode: 403, Code: "AccessDenied"}
		destination := newFlakyStore(map[string][]error{"object-000": {denied}})
		count, _, err := copyObjects(ctx, &quietLog{}, source, destination, "", "", 40,
			copyOptions{Concurrency: 4, Backoff: noBackoff})
		require.Error(t, err)
		require.Contains(t, err.Error(), "object-000")
		require.NotContains(t, err.Error(), "attempts", "a verdict is not retried")
		require.False(t, errors.Is(err, context.Canceled), "the first error is returned, not the cancellation it caused")
		require.Equal(t, 1, destination.attemptsFor("object-000"))
		require.Less(t, count, int64(40), "the remaining work was abandoned")
	})
}

func TestRetryableCopyError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"cancelled", context.Canceled, false},
		{"deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), false},
		{"not found", errNotFound, false},
		{"5xx", minio.ErrorResponse{StatusCode: 502, Code: "BadGateway"}, true},
		{"throttled", minio.ErrorResponse{StatusCode: 429}, true},
		{"slow down", minio.ErrorResponse{StatusCode: 200, Code: "SlowDownWrite"}, true},
		{"wrapped 503", fmt.Errorf("backup: put k: %w", minio.ErrorResponse{StatusCode: 503}), true},
		{"denied", minio.ErrorResponse{StatusCode: 403, Code: "AccessDenied"}, false},
		{"invalid", minio.ErrorResponse{StatusCode: 400, Code: "InvalidArgument"}, false},
		{"no such key", minio.ErrorResponse{StatusCode: 404, Code: "NoSuchKey"}, false},
		{"connection reset", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"eof", fmt.Errorf("read: %w", io.EOF), true},
		{"unknown", errors.New("something else"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, retryableCopyError(tc.err))
		})
	}
}

// TestMemoryStoreRemoveAll: the whole-bucket clear empties the store while
// RemovePrefix keeps refusing to do the same by accident.
func TestMemoryStoreRemoveAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newMemoryStore()
	fillStore(t, store, "a/", 3)
	fillStore(t, store, "b/", 2)

	_, err := store.RemovePrefix(ctx, "")
	require.Error(t, err, "the empty prefix stays refused")
	_, err = store.RemovePrefix(ctx, "/")
	require.Error(t, err)

	removed, err := store.RemoveAll(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 5, removed)
	require.Empty(t, store.keys(""))
	require.Equal(t, "remove-all", store.ops[len(store.ops)-1])
}
