package mirror

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

// testRegistry runs an in-memory OCI registry (plain http; 127.0.0.1 hosts
// resolve to the http scheme in go-containerregistry) and returns its
// host:port.
func testRegistry(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// testImporter wires an Importer between a fresh upstream and mirror
// registry pair over a real test database.
func testImporter(t *testing.T) (*Importer, string, string) {
	t.Helper()
	upstream, mirrorHost := testRegistry(t), testRegistry(t)
	st := store.NewStore(testdb.New(t))
	// Plain transport: the fake registries speak http, no mTLS to force.
	return &Importer{st: st, endpoint: mirrorHost, transport: http.DefaultTransport}, upstream, mirrorHost
}

func TestImportSingleImage(t *testing.T) {
	ctx := context.Background()
	imp, upstream, mirrorHost := testImporter(t)

	img, err := random.Image(1024, 3)
	require.NoError(t, err)
	src := fmt.Sprintf("%s/library/app:v1", upstream)
	srcTag, err := name.NewTag(src)
	require.NoError(t, err)
	require.NoError(t, remote.Write(srcTag, img))
	wantDigest, err := img.Digest()
	require.NoError(t, err)

	row, err := imp.Import(ctx, src)
	require.NoError(t, err)
	wantRepo := "mirror/" + strings.ReplaceAll(upstream, ":", "-") + "/library/app"
	require.Equal(t, wantRepo, row.Repository)
	require.Equal(t, "v1", row.Tag)
	require.Equal(t, wantDigest.String(), row.Digest, "the pin is the upstream digest")
	require.Positive(t, row.SizeBytes)

	// The mirror serves the copy byte-identical.
	dst, err := name.NewTag(fmt.Sprintf("%s/%s:v1", mirrorHost, wantRepo))
	require.NoError(t, err)
	desc, err := remote.Get(dst)
	require.NoError(t, err)
	require.Equal(t, wantDigest, desc.Digest)

	rows, err := imp.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestImportPreservesMultiArchIndex(t *testing.T) {
	ctx := context.Background()
	imp, upstream, mirrorHost := testImporter(t)

	idx, err := random.Index(512, 2, 3) // 3 platform manifests
	require.NoError(t, err)
	src := fmt.Sprintf("%s/library/multi:1.0", upstream)
	srcTag, err := name.NewTag(src)
	require.NoError(t, err)
	require.NoError(t, remote.WriteIndex(srcTag, idx))
	wantDigest, err := idx.Digest()
	require.NoError(t, err)

	row, err := imp.Import(ctx, src)
	require.NoError(t, err)
	require.Equal(t, wantDigest.String(), row.Digest)

	dst, err := name.NewTag(fmt.Sprintf("%s/%s:1.0", mirrorHost, row.Repository))
	require.NoError(t, err)
	desc, err := remote.Get(dst)
	require.NoError(t, err)
	require.Equal(t, wantDigest, desc.Digest, "index digest preserved")
	mirrored, err := desc.ImageIndex()
	require.NoError(t, err)
	manifest, err := mirrored.IndexManifest()
	require.NoError(t, err)
	require.Len(t, manifest.Manifests, 3, "every platform manifest survives the copy")
}

func TestReimportMovesThePin(t *testing.T) {
	ctx := context.Background()
	imp, upstream, _ := testImporter(t)

	src := fmt.Sprintf("%s/library/app:latest", upstream)
	srcTag, err := name.NewTag(src)
	require.NoError(t, err)

	img1, err := random.Image(256, 1)
	require.NoError(t, err)
	require.NoError(t, remote.Write(srcTag, img1))
	first, err := imp.Import(ctx, src)
	require.NoError(t, err)

	img2, err := random.Image(256, 2)
	require.NoError(t, err)
	require.NoError(t, remote.Write(srcTag, img2))
	second, err := imp.Import(ctx, src)
	require.NoError(t, err)

	require.Equal(t, first.ID, second.ID, "same (repository, tag) row")
	require.NotEqual(t, first.Digest, second.Digest, "the pin moved")
	require.Equal(t, first.ImportedAt, second.ImportedAt)
	require.True(t, second.UpdatedAt.After(first.UpdatedAt))

	rows, err := imp.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestImportErrorClasses(t *testing.T) {
	ctx := context.Background()
	imp, upstream, _ := testImporter(t)

	_, err := imp.Import(ctx, "UPPER CASE :::")
	require.ErrorIs(t, err, ErrInvalidReference)

	_, err = imp.Import(ctx, upstream+"/library/app@sha256:0000000000000000000000000000000000000000000000000000000000000000")
	require.ErrorIs(t, err, ErrInvalidReference, "digest refs have no tag to pin")

	_, err = imp.Import(ctx, upstream+"/library/ghost:1")
	require.ErrorIs(t, err, ErrUpstreamNotFound)

	// Seed a valid upstream image, then point the importer at a dead mirror.
	img, err := random.Image(256, 1)
	require.NoError(t, err)
	srcTag, err := name.NewTag(upstream + "/library/app:v1")
	require.NoError(t, err)
	require.NoError(t, remote.Write(srcTag, img))
	dead := httptest.NewServer(nil)
	deadHost := strings.TrimPrefix(dead.URL, "http://")
	dead.Close()
	imp.endpoint = deadHost
	_, err = imp.Import(ctx, upstream+"/library/app:v1")
	require.ErrorIs(t, err, ErrRegistryUnavailable)
}

func TestDeleteRemovesManifestAndRow(t *testing.T) {
	ctx := context.Background()
	imp, upstream, mirrorHost := testImporter(t)

	img, err := random.Image(256, 1)
	require.NoError(t, err)
	srcTag, err := name.NewTag(upstream + "/library/app:v1")
	require.NoError(t, err)
	require.NoError(t, remote.Write(srcTag, img))
	row, err := imp.Import(ctx, upstream+"/library/app:v1")
	require.NoError(t, err)

	require.NoError(t, imp.Delete(ctx, row.ID))

	rows, err := imp.List(ctx)
	require.NoError(t, err)
	require.Empty(t, rows)
	dst, err := name.NewDigest(fmt.Sprintf("%s/%s@%s", mirrorHost, row.Repository, row.Digest))
	require.NoError(t, err)
	_, err = remote.Get(dst)
	require.Error(t, err, "the manifest must be gone from the registry")

	// Unknown id → not found; a row whose manifest is already gone still
	// deletes cleanly (the registry 404 is tolerated).
	err = imp.Delete(ctx, uuid.New())
	require.ErrorIs(t, err, ErrImageNotFound)
}
