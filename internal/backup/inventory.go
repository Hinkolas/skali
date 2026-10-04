package backup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// bucketInventory replays the count pass for copying while object reads still
// use the live source. Only keys and sizes are spooled, keeping memory bounded
// even for large buckets. The file is unlinked immediately so cancellation,
// errors, and process termination cannot leave inventories behind.
type bucketInventory struct {
	objectStore
	file *os.File
}

func inventoryBucket(ctx context.Context, log copyLog, source objectStore) (*bucketInventory, int64, error) {
	file, err := os.CreateTemp("", "skali-backup-inventory-*")
	if err != nil {
		return nil, 0, fmt.Errorf("create bucket inventory: %w", err)
	}
	if err := os.Remove(file.Name()); err != nil {
		file.Close()
		return nil, 0, fmt.Errorf("unlink bucket inventory: %w", err)
	}
	inventory := &bucketInventory{objectStore: source, file: file}
	writer := bufio.NewWriter(file)
	encoder := json.NewEncoder(writer)
	var total int64
	var lastReport time.Time
	err = source.List(ctx, "", func(info objectInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := encoder.Encode(info); err != nil {
			return fmt.Errorf("write bucket inventory: %w", err)
		}
		total++
		if total%objectListPageSize == 0 && time.Since(lastReport) >= 5*time.Second {
			log.Info(ctx, fmt.Sprintf("counted %d objects; still listing bucket", total))
			lastReport = time.Now()
		}
		return nil
	})
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = writer.Flush()
	}
	if err != nil {
		inventory.Close()
		return nil, 0, err
	}
	return inventory, total, nil
}

func (s *bucketInventory) Close() error { return s.file.Close() }

func (s *bucketInventory) List(ctx context.Context, prefix string, fn func(objectInfo) error) error {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind bucket inventory: %w", err)
	}
	decoder := json.NewDecoder(bufio.NewReader(s.file))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var info objectInfo
		if err := decoder.Decode(&info); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read bucket inventory: %w", err)
		}
		if strings.HasPrefix(info.Key, prefix) {
			if err := fn(info); err != nil {
				return err
			}
		}
	}
}
