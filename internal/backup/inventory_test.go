package backup

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type countedListingStore struct {
	*memoryStore
	lists int
	err   error
}

func (s *countedListingStore) List(ctx context.Context, prefix string, fn func(objectInfo) error) error {
	s.lists++
	if s.err != nil {
		return s.err
	}
	return s.memoryStore.List(ctx, prefix, fn)
}

func TestBucketInventoryCopiesWithoutListingSourceTwice(t *testing.T) {
	ctx := context.Background()
	source := &countedListingStore{memoryStore: newMemoryStore()}
	meta := objectMeta{ContentType: "image/png", Tags: map[string]string{"kind": "original"}}
	require.NoError(t, source.PutWithMeta(ctx, "nested/image.png", strings.NewReader("image"), 5, meta))
	require.NoError(t, source.Put(ctx, "deleted.txt", strings.NewReader("old"), 3))
	log := &quietLog{}
	inventory, total, err := inventoryBucket(ctx, log, source)
	require.NoError(t, err)
	t.Cleanup(func() { inventory.Close() })
	require.EqualValues(t, 2, total)
	_, err = os.Stat(inventory.file.Name())
	require.ErrorIs(t, err, os.ErrNotExist, "the temporary inventory is already unlinked")

	// A live bucket may change after it was counted. Reads still hit the
	// source; a deleted key is skipped and newly created keys wait for the
	// next backup, rather than triggering another expensive count pass.
	require.NoError(t, source.Remove(ctx, "deleted.txt"))
	require.NoError(t, source.Put(ctx, "new.txt", strings.NewReader("new"), 3))
	target := newMemoryStore()
	count, size, err := copyObjects(ctx, log, inventory, target, "", "snapshot/", total,
		copyOptions{SkipMissing: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.EqualValues(t, 5, size)
	require.Equal(t, 1, source.lists)
	require.Equal(t, []string{"snapshot/nested/image.png"}, target.keys(""))
	reader, gotMeta, err := target.GetWithMeta(ctx, "snapshot/nested/image.png")
	require.NoError(t, err)
	reader.Close()
	require.Equal(t, meta, gotMeta)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, inventory.List(cancelled, "", func(objectInfo) error {
		t.Fatal("cancelled inventory must not emit objects")
		return nil
	}), context.Canceled)
}

func TestBucketInventoryReportsCountingAndCleansUpOnError(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	source := &countedListingStore{memoryStore: newMemoryStore()}
	for i := range objectListPageSize {
		source.objects[string(rune('a'+i))] = []byte("x")
	}
	log := &quietLog{}
	inventory, total, err := inventoryBucket(context.Background(), log, source)
	require.NoError(t, err)
	require.EqualValues(t, objectListPageSize, total)
	require.Contains(t, log.notes(), "counted 100 objects; still listing bucket")
	require.NoError(t, inventory.Close())
	source.err = errors.New("listing failed")
	inventory, _, err = inventoryBucket(context.Background(), log, source)
	require.ErrorIs(t, err, source.err)
	require.Nil(t, inventory)
	files, err := os.ReadDir(os.TempDir())
	require.NoError(t, err)
	require.Empty(t, files)
}
