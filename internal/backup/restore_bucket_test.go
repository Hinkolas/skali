package backup

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate"
)

type fenceProbe struct{ fenced, unfenced bool }

func (*fenceProbe) PlatformBucketAccess(context.Context, uuid.UUID, string) (substrate.BucketAccess, error) {
	return substrate.BucketAccess{}, nil
}
func (*fenceProbe) PlatformIdentityReady(context.Context) error { return nil }
func (f *fenceProbe) FenceBucket(context.Context, uuid.UUID, string) error {
	f.fenced = true
	return nil
}
func (f *fenceProbe) UnfenceBucket(context.Context, uuid.UUID, string) error {
	f.unfenced = true
	return nil
}

func TestRestoreBucketKeepsIncompleteSnapshotFenced(t *testing.T) {
	for _, tc := range []struct {
		name     string
		listed   int
		vanish   bool
		expected int64
		fail     bool
	}{
		{"complete", 2, false, 2, false},
		{"missing from listing", 1, false, 2, true},
		{"vanished after listing", 2, true, 2, true},
		{"unexpected objects in empty snapshot", 1, false, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source, destination := newMemoryStore(), newMemoryStore()
			fillStore(t, source, "snapshot/", tc.listed)
			fillStore(t, destination, "old/", 1)
			var snapshot objectStore = source
			if tc.vanish {
				snapshot = &vanishingStore{memoryStore: source, gone: "snapshot/object-000"}
			}
			fence := &fenceProbe{}
			c := &Controller{deps: Deps{Buckets: fence}, openBucket: func(context.Context, uuid.UUID, string) (objectStore, string, error) { return destination, "live", nil }}
			err := c.restoreBucket(context.Background(), &quietLog{}, &backupContext{row: &store.Backup{EnvironmentID: uuid.New()}, target: snapshot}, Component{ServiceKey: "files", ObjectPrefix: "snapshot/", ObjectCount: tc.expected})
			require.True(t, fence.fenced)
			require.False(t, destination.has("old/object-000"))
			if tc.fail {
				require.Error(t, err)
				require.False(t, fence.unfenced, "incomplete restores must not reopen the bucket")
			} else {
				require.NoError(t, err)
				require.True(t, fence.unfenced)
			}
		})
	}
}

// lossyStore acknowledges a write of dropped without keeping it, and
// writes stray beside the first object it is given: the gateway losing an
// object, and a writer landing one, while the copy counts all its puts.
type lossyStore struct {
	*memoryStore
	dropped string
	stray   string
	landed  sync.Once
}

func (s *lossyStore) PutWithMeta(ctx context.Context, key string, r io.Reader, size int64, meta objectMeta) error {
	if key == s.dropped {
		_, err := io.Copy(io.Discard, r)
		return err
	}
	if s.stray != "" {
		var err error
		s.landed.Do(func() { err = s.memoryStore.Put(ctx, s.stray, strings.NewReader("x"), 1) })
		if err != nil {
			return err
		}
	}
	return s.memoryStore.PutWithMeta(ctx, key, r, size, meta)
}

// The copy's own count cannot tell a restored bucket from one the gateway
// lost an object of, or one an object landed in beside the copy: the
// restore lists the bucket again and keeps it fenced unless it holds
// exactly the snapshot's keys at their sizes.
func TestRestoreBucketVerifiesTheRestoredListing(t *testing.T) {
	for _, tc := range []struct {
		name           string
		dropped, stray string
		fail           bool
	}{
		{name: "exact"},
		{name: "object lost", dropped: "object-001", fail: true},
		{name: "object landed", stray: "stray", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := newMemoryStore()
			fillStore(t, source, "snapshot/", 3)
			destination := &lossyStore{memoryStore: newMemoryStore(), dropped: tc.dropped, stray: tc.stray}
			fence := &fenceProbe{}
			c := &Controller{deps: Deps{Buckets: fence}, openBucket: func(context.Context, uuid.UUID, string) (objectStore, string, error) {
				return destination, "live", nil
			}}
			component := Component{Kind: ComponentBucket, ServiceKey: "files", ObjectPrefix: "snapshot/", ObjectCount: 3}
			bctx := &backupContext{row: &store.Backup{EnvironmentID: uuid.New()}, target: source}
			listing, err := listBucket(context.Background(), source, "snapshot/")
			require.NoError(t, err)
			component.Bytes = listing.Bytes
			bctx.listings = map[string]bucketListing{componentLabel(component): listing}
			err = c.restoreBucket(context.Background(), &quietLog{}, bctx, component)
			if tc.fail {
				require.ErrorContains(t, err, "the bucket remains fenced")
				require.False(t, fence.unfenced, "a bucket that differs from its snapshot must stay fenced")
				return
			}
			require.NoError(t, err)
			require.True(t, fence.unfenced)
		})
	}
}
