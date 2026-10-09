package substrate

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/workstats"
)

const (
	// defaultAbortUploadsAfter is the sweep threshold for multipart
	// uploads of a bucket that declares no
	// lifecycle.abortIncompleteUploadsAfter.
	defaultAbortUploadsAfter = 24 * time.Hour

	// maintenanceInterval paces the storage maintenance loop; quota
	// enforcement is approximate by one interval plus in-flight uploads
	// (documented product behavior).
	maintenanceInterval = 15 * time.Second
	// quotaTimeout bounds the quota step of one pass: the volume listing
	// and the single filer.conf write, which may wait behind an identity
	// write's shell exec.
	quotaTimeout = 90 * time.Second
	// bucketMaintenanceTimeout bounds the work on one bucket, so a slow
	// bucket delays the pass without starving the others.
	bucketMaintenanceTimeout = 90 * time.Second
)

// runStorageMaintenance runs the store's periodic upkeep until the
// context ends. It is separate from the observation probe on purpose: the
// upkeep issues several admin requests per bucket and takes the admin
// locks, and its slowness or failure must never make storage health look
// stale. Every failure is logged and retried on the next pass.
func (c *Controller) runStorageMaintenance(ctx context.Context) {
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pass := workstats.NewPass("maintenance")
		c.maintainStorage(workstats.WithPass(ctx, pass))
		pass.Log(ctx, "substrate: storage maintenance", 0)
	}
}

// maintainStorage runs one upkeep pass: quota enforcement for every
// bucket first (one volume listing, one filer.conf write), then each
// bucket's fence, rotation, drift and upload-sweep work under its own
// timeout.
func (c *Controller) maintainStorage(ctx context.Context) {
	if c.deps.Seaweed == nil {
		return
	}
	row, err := c.deps.DB.LiveObjectStore(ctx)
	if errors.Is(err, dbstore.ErrNotFound) {
		return
	}
	if err != nil {
		slog.Warn("substrate: storage maintenance", "error", err)
		return
	}
	if row.State == dbstore.StateReleasing || row.State == dbstore.StateReleased {
		return
	}
	claims, allocations, err := c.storeBuckets(ctx, row.ID)
	if err != nil {
		slog.Warn("substrate: storage maintenance", "error", err)
		return
	}

	if err := c.enforceQuotas(ctx, claims, allocations); err != nil {
		slog.Warn("substrate: enforce storage quotas", "error", err)
	}

	live := make(map[string]bool, len(allocations))
	for _, claimRow := range claims {
		allocation, ok := allocations[claimRow.ID]
		if !ok {
			continue
		}
		live[allocation.BucketName] = true
		bucketCtx, cancel := context.WithTimeout(ctx, bucketMaintenanceTimeout)
		c.maintainBucket(bucketCtx, claimRow, allocation)
		cancel()
		if ctx.Err() != nil {
			return
		}
	}
	c.pruneBucketDrift(live)
}

// enforceQuotas reconciles every bucket's read-only flag against its
// usage in one filer.conf read-modify-write: at or over quota the bucket
// path turns read-only, back under it reopens. A fenced bucket is never
// read-only: the flag is path-wide and would refuse the restore's own
// writes; it is re-evaluated once the fence lifts. Filers hot-reload the
// document; the probe is poked so the new state shows without waiting a
// full interval.
func (c *Controller) enforceQuotas(ctx context.Context, claims []store.BucketClaim, allocations map[uuid.UUID]store.BucketAllocation) error {
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	sizes, err := c.deps.Seaweed.CollectionSizes(ctx)
	if err != nil {
		return err
	}
	readOnly := make(map[string]bool, len(claims))
	for _, claimRow := range claims {
		allocation, ok := allocations[claimRow.ID]
		if !ok {
			continue
		}
		used := sizes[allocation.BucketName].LiveBytes
		readOnly[allocation.BucketName] = allocation.FencedAt == nil &&
			claimRow.StorageQuotaBytes > 0 && used >= claimRow.StorageQuotaBytes
	}
	changed, err := c.deps.Seaweed.SetReadOnlyPaths(ctx, readOnly)
	if err != nil {
		return err
	}
	if changed {
		c.pokeProbe()
	}
	return nil
}

// maintainBucket runs one bucket's upkeep; each step logs its own failure
// and the next one still runs.
func (c *Controller) maintainBucket(ctx context.Context, claimRow store.BucketClaim, allocation store.BucketAllocation) {
	bucket := allocation.BucketName
	// A fence outlives a failed restore only while its environment is
	// down; a deploy that brings the environment back finds the bucket
	// reachable within one interval.
	if err := c.liftStaleFence(ctx, claimRow, &allocation); err != nil {
		slog.Warn("substrate: lift stale bucket fence", "bucket", bucket, "error", err)
	}
	// A rotation's previous keypair retires on the claim worker's pass,
	// the one writer of credential Secrets; upkeep only notices the
	// instant has passed and wakes it.
	if allocation.CredentialRetireAt != nil && !time.Now().Before(*allocation.CredentialRetireAt) {
		c.EnqueueBucketClaim(claimRow.ID, reasonMaintenance)
	}
	// Drift repair: a setting an application changed behind the platform
	// is reset within one interval and reported as the audit trail. Before
	// the platform identity is loaded (a fresh process, the store still
	// reconciling) there is nothing to check yet, and nothing to sweep
	// with.
	desiredCORS, err := seaweed.CORSConfig(claimRow.Cors)
	if err != nil {
		slog.Warn("substrate: decode bucket cors", "bucket", bucket, "error", err)
		return
	}
	drift, err := c.deps.Seaweed.EnsureBucketConfiguration(ctx, bucket, seaweed.BucketPolicy(bucket), desiredCORS)
	if errors.Is(err, seaweed.ErrNoPlatformCredentials) {
		return
	}
	if err != nil {
		slog.Warn("substrate: check bucket configuration", "bucket", bucket, "error", err)
	} else {
		if len(drift) > 0 {
			slog.Info("substrate: bucket configuration reset", "bucket", bucket, "settings", drift)
		}
		c.setBucketDrift(bucket, drift)
	}
	// Stale multipart uploads are swept on the same cadence: parts a
	// browser never completed would otherwise count against the quota
	// forever. The threshold is the bucket's declared
	// lifecycle.abortIncompleteUploadsAfter, a day when unset.
	threshold := defaultAbortUploadsAfter
	if claimRow.AbortUploadsAfterSeconds > 0 {
		threshold = time.Duration(claimRow.AbortUploadsAfterSeconds) * time.Second
	}
	aborted, err := c.deps.Seaweed.AbortStaleUploads(ctx, bucket, threshold)
	if err != nil {
		slog.Warn("substrate: sweep stale uploads", "bucket", bucket, "error", err)
	} else if aborted > 0 {
		slog.Info("substrate: stale multipart uploads aborted", "bucket", bucket,
			"count", aborted, "olderThan", threshold.String())
	}
}

// bucketDrift returns the settings the last upkeep pass had to reset on
// the bucket, for the probe to report.
func (c *Controller) bucketDrift(bucket string) []string {
	c.driftMu.Lock()
	defer c.driftMu.Unlock()
	return c.drift[bucket]
}

func (c *Controller) setBucketDrift(bucket string, drift []string) {
	c.driftMu.Lock()
	defer c.driftMu.Unlock()
	if len(drift) == 0 {
		delete(c.drift, bucket)
		return
	}
	if c.drift == nil {
		c.drift = map[string][]string{}
	}
	c.drift[bucket] = drift
}

// pruneBucketDrift forgets buckets that are no longer live.
func (c *Controller) pruneBucketDrift(live map[string]bool) {
	c.driftMu.Lock()
	defer c.driftMu.Unlock()
	for bucket := range c.drift {
		if !live[bucket] {
			delete(c.drift, bucket)
		}
	}
}

// enforceBucketQuota reconciles one bucket's read-only flag against its
// usage, the single-bucket form of enforceQuotas for a fence and a
// restore that must settle the flag in their own pass.
func (c *Controller) enforceBucketQuota(ctx context.Context, quotaBytes int64, bucket string, usedBytes int64, fenced bool) (bool, error) {
	over := !fenced && quotaBytes > 0 && usedBytes >= quotaBytes
	_, err := c.deps.Seaweed.SetReadOnlyPaths(ctx, map[string]bool{bucket: over})
	return over, err
}
