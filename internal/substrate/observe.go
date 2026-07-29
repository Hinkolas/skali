package substrate

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

var (
	objectStoreGVK = schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "ObjectStore"}
	bucketGVK      = schema.GroupVersionKind{Group: "seaweed.skali.dev", Version: "v1", Kind: "Bucket"}
)

// SeaweedProbe returns the provider observation probe (REWORK_V2 7.4): one
// pass reports the platform-scoped store status plus per-bucket existence
// and usage, and enforces storage quotas by flipping per-bucket read-only
// flags on the same cadence. A stopped store reports by intent without
// contacting seaweed, so dev idleness never reads as staleness.
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
				Stopped:              row.State == dbstore.StateStopped,
			},
		}
		if row.State == dbstore.StateStopped {
			return []observe.Object{storeObj}, nil
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
		storeObj.ObjectStore.S3Ready = c.deps.Seaweed.S3Alive(ctx)

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
			stat := sizes[allocation.BucketName]
			readOnly, err := c.enforceBucketQuota(ctx, claimRow.StorageQuotaBytes, allocation.BucketName, stat.SizeBytes)
			if err != nil {
				return nil, err
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
					Exists:      exists,
					UsedBytes:   stat.SizeBytes,
					ObjectCount: stat.FileCount,
					QuotaBytes:  claimRow.StorageQuotaBytes,
					ReadOnly:    readOnly,
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
func (c *Controller) enforceBucketQuota(ctx context.Context, quotaBytes int64, bucket string, usedBytes int64) (bool, error) {
	over := quotaBytes > 0 && usedBytes >= quotaBytes
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
