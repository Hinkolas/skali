package substrate

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

var (
	objectStoreGVK = schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "ObjectStore"}
	bucketGVK      = schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "Bucket"}
)

// SeaweedProbe returns the provider observation probe: one pass reports
// the platform-scoped store status plus per-bucket existence, usage and
// read-only state. It only observes: everything it reads comes from a
// fixed handful of requests however many buckets exist (one bucket
// listing, one filer.conf read, one volume listing), and it takes no
// admin lock, so it can neither outgrow its timeout nor queue behind an
// identity write. Quota enforcement, drift repair, the upload sweep and
// fence lifting run in the maintenance loop (maintenance.go).
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
		sizes, health, err := c.deps.Seaweed.VolumeReport(ctx, row.Replication)
		if err != nil {
			return nil, err
		}
		storeObj.ObjectStore.UnderReplicatedVolumes = int32(health.UnderReplicated)
		storeObj.ObjectStore.ReplicationPendingVolumes = int32(health.Unconfigured)
		if c.cfg.Managed {
			placement, err := c.placementShortfalls(ctx, *row)
			if err != nil {
				return nil, err
			}
			storeObj.ObjectStore.Placement = placement
		}

		existing, err := c.deps.Seaweed.BucketNames(ctx)
		if err != nil {
			return nil, err
		}
		conf, err := c.deps.Seaweed.Conf(ctx)
		if err != nil {
			return nil, err
		}
		claims, allocations, err := c.storeBuckets(ctx, row.ID)
		if err != nil {
			return nil, err
		}

		objects := []observe.Object{storeObj}
		for _, claimRow := range claims {
			allocation, ok := allocations[claimRow.ID]
			if !ok || claimRow.OwnerKind != dbstore.OwnerService || claimRow.EnvironmentID == nil {
				continue
			}
			stat := sizes[allocation.BucketName]
			readOnly := false
			if entry := conf.Find(seaweed.BucketsPrefix + allocation.BucketName + "/"); entry != nil {
				readOnly = entry.ReadOnly
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
					Exists:             existing[allocation.BucketName],
					UsedBytes:          stat.LiveBytes,
					DiskBytes:          stat.SizeBytes,
					EntryCount:         stat.EntryCount,
					QuotaBytes:         claimRow.StorageQuotaBytes,
					ReadOnly:           readOnly,
					ConfigurationDrift: c.bucketDrift(allocation.BucketName),
				},
			})
		}
		return objects, nil
	}
}

// storeBuckets reads the store's bucket claims and their live allocations
// keyed by claim, two queries for any number of buckets.
func (c *Controller) storeBuckets(ctx context.Context, storeID uuid.UUID) ([]store.BucketClaim, map[uuid.UUID]store.BucketAllocation, error) {
	claims, err := c.deps.DB.ListStoreBucketClaims(ctx, storeID)
	if err != nil {
		return nil, nil, err
	}
	rows, err := c.deps.DB.ListStoreAllocations(ctx, storeID)
	if err != nil {
		return nil, nil, err
	}
	allocations := make(map[uuid.UUID]store.BucketAllocation, len(rows))
	for _, row := range rows {
		allocations[row.ClaimID] = row
	}
	return claims, allocations, nil
}
