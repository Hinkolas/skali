package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/Hinkolas/skali/internal/utils"
)

// applyRetention deletes the scheduled snapshots of this environment once
// they are older than the retention window. The newest scheduled snapshot
// is always kept, as is anything an unfinished restore reads; manual
// snapshots are never candidates. Nothing is deleted unless the listing
// completed and every manifest decoded: a partial view must not drive
// deletions.
func (c *Controller) applyRetention(ctx context.Context, log *stepLog, bctx *backupContext, retention time.Duration, now time.Time) error {
	if retention <= 0 {
		log.Info(ctx, "retention is not set; keeping every snapshot")
		return nil
	}
	row, target := bctx.row, bctx.target
	prefix := snapshotPrefix(bctx.prefix(), row.ProjectName, row.EnvironmentName)
	var keys []string
	if err := target.List(ctx, prefix, func(info objectInfo) error {
		keys = append(keys, info.Key)
		return nil
	}); err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	manifests := make(map[string]*Manifest, len(keys))
	var listed []*Manifest
	for _, key := range keys {
		manifest, err := c.readManifest(ctx, target, key)
		if err != nil {
			var unsupported *UnsupportedManifestError
			if errors.As(err, &unsupported) {
				slog.WarnContext(ctx, "backup: retention skips unsupported manifest", "key", key, "err", err)
				continue
			}
			return fmt.Errorf("read manifest %s: %w", key, err)
		}
		manifests[manifest.SnapshotID] = manifest
		listed = append(listed, manifest)
	}
	protected, err := c.snapshotsBeingRestored(ctx)
	if err != nil {
		return err
	}

	cutoff := now.Add(-retention)
	candidates := retentionCandidates(listed, cutoff, protected)
	log.Info(ctx, fmt.Sprintf("scheduled snapshots are kept for %s; %d older than %s to delete",
		formatRetention(retention), len(candidates), cutoff.Format(time.RFC3339)))
	var deleted int
	var freed int64
	for _, manifest := range candidates {
		key := manifestKey(bctx.prefix(), row.ProjectName, manifest.Environment, manifest.SnapshotID)
		var bytes int64
		for _, component := range manifest.Components {
			bytes += component.Bytes
		}
		log.Info(ctx, fmt.Sprintf("deleting snapshot %s from %s (%s)",
			manifest.SnapshotID, manifest.CreatedAt.Format(time.RFC3339), utils.FormatBytes(bytes)))
		if err := deleteSnapshotObjects(ctx, target, key, manifest); err != nil {
			return fmt.Errorf("delete snapshot %s: %w", manifest.SnapshotID, err)
		}
		deleted++
		freed += bytes
	}
	log.Info(ctx, fmt.Sprintf("kept %d snapshot(s), deleted %d (%s)",
		len(listed)-deleted, deleted, utils.FormatBytes(freed)))
	return nil
}

// retentionCandidates picks the snapshots retention deletes: scheduled
// snapshots older than the cutoff, never the newest scheduled one and never
// a protected id. Pure so it is tested alone.
func retentionCandidates(manifests []*Manifest, cutoff time.Time, protected map[string]bool) []*Manifest {
	var owned []*Manifest
	for _, manifest := range manifests {
		if manifest.Trigger == TriggerScheduled {
			owned = append(owned, manifest)
		}
	}
	sort.Slice(owned, func(i, j int) bool {
		return owned[i].CreatedAt.After(owned[j].CreatedAt)
	})
	var candidates []*Manifest
	for i, manifest := range owned {
		if i == 0 {
			continue
		}
		if !manifest.CreatedAt.Before(cutoff) || protected[manifest.SnapshotID] {
			continue
		}
		candidates = append(candidates, manifest)
	}
	return candidates
}

// formatRetention words a retention window in whole days or hours.
func formatRetention(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int64(d/(24*time.Hour)))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int64(d/time.Hour))
	default:
		return fmt.Sprintf("%dm", int64(d/time.Minute))
	}
}
