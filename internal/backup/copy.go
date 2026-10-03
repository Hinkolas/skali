package backup

import (
	"context"
	"fmt"
)

// copyLog is the narration a copy emits; *journal.StepLog in production.
type copyLog interface {
	Info(ctx context.Context, message string)
	Progress(ctx context.Context, current, total int64)
}

// copyObjects streams every object under sourcePrefix in source to
// destinationPrefix in destination, metadata included, reporting progress
// against expected (the listing's count when known, otherwise zero) and
// returning what it copied. It runs in-process: skalid has pod-network
// reach to both stores and streaming Get to Put keeps memory flat.
func copyObjects(ctx context.Context, log copyLog, source, destination objectStore,
	sourcePrefix, destinationPrefix string, expected int64) (count, bytes int64, err error) {
	err = source.List(ctx, sourcePrefix, func(info objectInfo) error {
		reader, meta, err := source.GetWithMeta(ctx, info.Key)
		if err != nil {
			return err
		}
		defer reader.Close()
		key := destinationPrefix + info.Key[len(sourcePrefix):]
		if err := destination.PutWithMeta(ctx, key, reader, info.Size, meta); err != nil {
			return err
		}
		count++
		bytes += info.Size
		if count%16 == 0 || count == expected {
			log.Progress(ctx, count, expected)
		}
		return nil
	})
	if err != nil {
		return count, bytes, err
	}
	if expected > 0 && count != expected {
		// The bucket drifted during the copy: the documented loose
		// consistency of a live snapshot, worth a line in the log.
		log.Info(ctx, fmt.Sprintf("listing changed during the copy: expected %d objects, copied %d", expected, count))
	}
	return count, bytes, nil
}
