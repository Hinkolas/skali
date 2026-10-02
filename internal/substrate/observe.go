package substrate

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// defaultAbortUploadsAfter is the sweep threshold for multipart uploads
// of a bucket that declares no lifecycle.abortIncompleteUploadsAfter.
const defaultAbortUploadsAfter = 24 * time.Hour

var (
	objectStoreGVK = schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "ObjectStore"}
	bucketGVK      = schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "Bucket"}
)

// SeaweedProbe returns the provider observation probe: one
// pass reports the platform-scoped store status plus per-bucket existence
// and usage, and enforces storage quotas by flipping per-bucket read-only
// flags on the same cadence.
func (c *Controller) SeaweedProbe() observe.Probe {
	return func(ctx context.Context) ([]observe.Object, error) {
		row, err := c.deps.DB.LiveObjectStore(ctx)
		if errors.Is(err, dbstore.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if row.State == dbstore.StateReleasing || row.State == dbstore.StateReleased {
			return nil, nil
		}

		storeObj := observe.Object{
			Ref:       kube.ObjectRef{GVK: objectStoreGVK, Name: seaweed.StoreName},
			Kind:      module.KindObjectStore,
			Name:      seaweed.StoreName,
			SharedKey: seaweed.SharedKey,
			ObjectStore: &module.ObjectStoreStatus{
				MastersDesired:       row.Masters,
				VolumeServersDesired: row.VolumeServers,
			},
		}
		if c.deps.Seaweed == nil {
			return nil, errors.New("substrate: no seaweed client configured")
		}

		_, peers, err := c.deps.Seaweed.ClusterStatus(ctx)
		if err != nil {
			return nil, err
		}
		ready := int32(len(peers) + 1)
		if ready > row.Masters {
			ready = row.Masters
		}
		storeObj.ObjectStore.MastersReady = ready
		if count, err := c.deps.Seaweed.VolumeServerCount(ctx); err == nil {
			storeObj.ObjectStore.VolumeServersReady = int32(count)
		}
		storeObj.ObjectStore.FilerReady = c.deps.Seaweed.FilerAlive(ctx)
		storeObj.ObjectStore.S3Ready, storeObj.ObjectStore.S3Detail = c.deps.Seaweed.S3Ready(ctx)
		if health, err := c.deps.Seaweed.VolumeHealth(ctx, row.Replication); err == nil {
			storeObj.ObjectStore.UnderReplicatedVolumes = int32(health.UnderReplicated)
			storeObj.ObjectStore.ReplicationPendingVolumes = int32(health.Unconfigured)
		}
		if c.cfg.Managed {
			placement, err := c.placementShortfalls(ctx, *row)
			if err != nil {
				return nil, err
			}
			storeObj.ObjectStore.Placement = placement
		}

		sizes, err := c.deps.Seaweed.CollectionSizes(ctx)
		if err != nil {
			return nil, err
		}
		claims, err := c.deps.DB.ListStoreBucketClaims(ctx, row.ID)
		if err != nil {
			return nil, err
		}

		objects := []observe.Object{storeObj}
		for _, claimRow := range claims {
			allocation, err := c.deps.DB.LiveAllocation(ctx, claimRow.ID)
			if err != nil {
				if errors.Is(err, dbstore.ErrNotFound) {
					continue
				}
				return nil, err
			}
			exists, err := c.deps.Seaweed.BucketExists(ctx, allocation.BucketName)
			if err != nil {
				return nil, err
			}
			// A fence outlives a failed restore only while its environment
			// is down; a deploy that brings the environment back finds the
			// bucket reachable within one probe interval.
			if err := c.liftStaleFence(ctx, claimRow, allocation); err != nil {
				return nil, err
			}
			stat := sizes[allocation.BucketName]
			readOnly, err := c.enforceBucketQuota(ctx, claimRow.StorageQuotaBytes, allocation.BucketName,
				stat.LiveBytes, allocation.FencedAt != nil)
			if err != nil {
				return nil, err
			}
			// Drift repair rides the probe like quota enforcement does: a
			// setting an application changed behind the platform is reset
			// within one interval and reported as the audit trail. Before
			// the platform identity is loaded (a fresh process, the store
			// still reconciling) there is nothing to check yet.
			desiredCORS, err := seaweed.CORSConfig(claimRow.Cors)
			if err != nil {
				return nil, err
			}
			drift, err := c.deps.Seaweed.EnsureBucketConfiguration(ctx, allocation.BucketName,
				seaweed.BucketPolicy(allocation.BucketName), desiredCORS)
			if err != nil && !errors.Is(err, seaweed.ErrNoPlatformCredentials) {
				return nil, err
			}
			// Stale multipart uploads are swept on the same cadence: parts
			// a browser never completed would otherwise count against the
			// quota forever. The threshold is the bucket's declared
			// lifecycle.abortIncompleteUploadsAfter, a day when unset.
			threshold := defaultAbortUploadsAfter
			if claimRow.AbortUploadsAfterSeconds > 0 {
				threshold = time.Duration(claimRow.AbortUploadsAfterSeconds) * time.Second
			}
			if err == nil {
				aborted, sweepErr := c.deps.Seaweed.AbortStaleUploads(ctx, allocation.BucketName, threshold)
				if sweepErr != nil {
					slog.Warn("substrate: sweep stale uploads", "bucket", allocation.BucketName, "error", sweepErr)
				} else if aborted > 0 {
					slog.Info("substrate: stale multipart uploads aborted", "bucket", allocation.BucketName,
						"count", aborted, "olderThan", threshold.String())
				}
			}
			if claimRow.OwnerKind != dbstore.OwnerService || claimRow.EnvironmentID == nil {
				continue
			}
			service := "buckets." + claimRow.ServiceKey
			objects = append(objects, observe.Object{
				Ref:         kube.ObjectRef{GVK: bucketGVK, Name: allocation.BucketName},
				Kind:        module.KindBucket,
				Name:        service,
				Environment: *claimRow.EnvironmentID,
				Service:     service,
				SharedKey:   seaweed.SharedKey,
				Bucket: &module.BucketStatus{
					Exists:             exists,
					UsedBytes:          stat.LiveBytes,
					DiskBytes:          stat.SizeBytes,
					EntryCount:         stat.EntryCount,
					QuotaBytes:         claimRow.StorageQuotaBytes,
					ReadOnly:           readOnly,
					ConfigurationDrift: drift,
				},
			})
		}
		return objects, nil
	}
}

// enforceBucketQuota reconciles one bucket's read-only flag against its
// usage: at or over quota the bucket path turns read-only, back under it
// reopens. Enforcement is approximate by one poll interval plus in-flight
// uploads (documented product behavior); filers hot-reload the document.
// A fenced bucket is never read-only: the flag is path-wide and would
// refuse the restore's own writes; it is re-evaluated once the fence
// lifts.
func (c *Controller) enforceBucketQuota(ctx context.Context, quotaBytes int64, bucket string, usedBytes int64, fenced bool) (bool, error) {
	over := !fenced && quotaBytes > 0 && usedBytes >= quotaBytes
	prefix := seaweed.BucketsPrefix + bucket + "/"
	err := c.deps.Seaweed.UpdateConf(ctx, func(conf *seaweed.FilerConf) bool {
		entry := conf.Find(prefix)
		switch {
		case over && entry == nil:
			conf.Locations = append(conf.Locations, seaweed.PathConf{LocationPrefix: prefix, ReadOnly: true})
			return true
		case over && !entry.ReadOnly:
			entry.ReadOnly = true
			return true
		case !over && entry != nil && entry.ReadOnly:
			entry.ReadOnly = false
			return true
		}
		return false
	})
	return over, err
}
