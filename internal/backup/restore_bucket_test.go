package backup

import (
	"context"
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
