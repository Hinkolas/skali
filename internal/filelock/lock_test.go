package filelock

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSharedLeaseBlocksPublicationAndPruning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release.lock")
	first, err := Shared(context.Background(), path)
	require.NoError(t, err)
	second, err := Shared(context.Background(), path)
	require.NoError(t, err)
	exclusive, err := Try(path)
	require.NoError(t, err)
	require.Nil(t, exclusive)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = Acquire(ctx, path)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	first()
	exclusive, err = Try(path)
	require.NoError(t, err)
	require.Nil(t, exclusive)
	second()
	exclusive, err = Try(path)
	require.NoError(t, err)
	require.NotNil(t, exclusive)
	exclusive()
}
