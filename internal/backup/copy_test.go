package backup

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type quietLog struct{ progress []int64 }

func (quietLog) Info(context.Context, string) {}
func (l *quietLog) Progress(_ context.Context, current, _ int64) {
	l.progress = append(l.progress, current)
}

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
	count, bytes, err := copyObjects(ctx, log, source, destination, "snap/1/", "", 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
	require.EqualValues(t, 14, bytes)
	require.Equal(t, []int64{2}, log.progress, "progress reports on the final object")

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
	count, _, err = copyObjects(ctx, log, destination, back, "", "restored/", 0)
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
	_, got, err = back.GetWithMeta(ctx, "restored/photos/a.png")
	require.NoError(t, err)
	require.Equal(t, meta, got)
}
