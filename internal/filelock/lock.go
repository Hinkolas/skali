// Package filelock serializes access to user-owned state across processes.
package filelock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Acquire locks a stable sidecar inode. Never remove the lock file: unlinking
// it would let another process lock a different inode for the same resource.
func Acquire(ctx context.Context, path string) (func(), error) {
	return acquire(ctx, path, false, false)
}

func Shared(ctx context.Context, path string) (func(), error) { return acquire(ctx, path, true, false) }

// Try acquires an exclusive lock without waiting. nil unlock means busy.
func Try(path string) (func(), error) { return acquire(context.Background(), path, false, true) }

func acquire(ctx context.Context, path string, shared, once bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		ok, err := tryLock(f, shared)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if ok {
			return func() { f.Close() }, nil
		}
		if once {
			f.Close()
			return nil, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
