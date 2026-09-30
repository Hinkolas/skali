package substrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// BucketAccess is what the backup engine needs to speak S3 to one
// service's bucket as the platform: the in-cluster gateway, the bucket,
// and the platform identity's keypair. It never carries the bucket's own
// credentials, which a restore deletes.
type BucketAccess struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

// PlatformBucketAccess resolves the service's live bucket and the platform
// keypair. Backups read and restores write through it, so neither depends
// on the bucket identity (fenced during a restore) or on the quota flag
// (lifted while fenced).
func (c *Controller) PlatformBucketAccess(ctx context.Context, environmentID uuid.UUID, serviceKey string) (BucketAccess, error) {
	allocation, err := c.serviceAllocation(ctx, environmentID, serviceKey)
	if err != nil {
		return BucketAccess{}, err
	}
	secret, err := c.deps.Cluster.GetSecret(ctx, Namespace, PlatformCredentialSecret)
	if err != nil {
		return BucketAccess{}, fmt.Errorf("substrate: read platform credentials: %w", err)
	}
	return BucketAccess{
		Endpoint:  InternalBucketEndpoint(),
		Region:    allocation.Region,
		Bucket:    allocation.BucketName,
		AccessKey: string(secret.Data["access_key"]),
		SecretKey: string(secret.Data["secret_key"]),
	}, nil
}

// FenceBucket keeps every credential the environment holds away from the
// bucket while a restore rewrites it: the allocation is marked fenced,
// the bucket's identity is deleted (mirrored keys and every presigned URL
// signed with them fail from here on), and the quota flag is lifted so the
// restore's own writes are not refused by a bucket that was full. The
// platform identity keeps writing. Fencing an already fenced bucket is a
// no-op, so a re-run restore is safe.
func (c *Controller) FenceBucket(ctx context.Context, environmentID uuid.UUID, serviceKey string) error {
	allocation, err := c.serviceAllocation(ctx, environmentID, serviceKey)
	if err != nil {
		return err
	}
	if c.deps.Seaweed == nil {
		return errors.New("substrate: the object-storage substrate is not available")
	}
	if err := c.deps.DB.FenceAllocation(ctx, allocation.ID); err != nil {
		return err
	}
	if err := c.deps.Seaweed.DeleteIdentity(ctx, allocation.BucketName); err != nil {
		return fmt.Errorf("substrate: fence %s: %w", allocation.BucketName, err)
	}
	if _, err := c.enforceBucketQuota(ctx, 0, allocation.BucketName, 0, true); err != nil {
		return fmt.Errorf("substrate: lift quota flag for %s: %w", allocation.BucketName, err)
	}
	slog.Info("substrate: bucket fenced", "bucket", allocation.BucketName, "service", serviceKey)
	c.pokeProbe()
	return nil
}

// UnfenceBucket restores the bucket's identity with its unchanged keypair
// and clears the fence. Presigned URLs signed before the fence work again
// once they do (the keypair is the same); only a rotation retires them.
// The quota flag is re-evaluated by the next probe.
func (c *Controller) UnfenceBucket(ctx context.Context, environmentID uuid.UUID, serviceKey string) error {
	allocation, err := c.serviceAllocation(ctx, environmentID, serviceKey)
	if err != nil {
		return err
	}
	return c.unfence(ctx, allocation)
}

func (c *Controller) unfence(ctx context.Context, allocation *store.BucketAllocation) error {
	if c.deps.Seaweed == nil {
		return errors.New("substrate: the object-storage substrate is not available")
	}
	secret, err := c.deps.Cluster.GetSecret(ctx, Namespace, allocation.CredentialSecret)
	if err != nil {
		return fmt.Errorf("substrate: read bucket credentials: %w", err)
	}
	if err := c.deps.Seaweed.EnsureIdentity(ctx, seaweed.Identity{
		Name: allocation.BucketName,
		Credentials: []seaweed.Credential{{
			AccessKey: string(secret.Data["access_key"]),
			SecretKey: string(secret.Data["secret_key"]),
		}},
		Actions: seaweed.BucketActions(allocation.BucketName),
	}); err != nil {
		return fmt.Errorf("substrate: unfence %s: %w", allocation.BucketName, err)
	}
	if err := c.deps.DB.UnfenceAllocation(ctx, allocation.ID); err != nil {
		return err
	}
	slog.Info("substrate: bucket fence lifted", "bucket", allocation.BucketName)
	c.pokeProbe()
	return nil
}

// liftStaleFence lifts a fence whose environment is no longer down: a
// restore that failed leaves the environment down and the bucket fenced
// on purpose (re-running the restore is the way forward), but a deploy
// that brings the environment back must find its bucket reachable again.
// A fence on a running restore is never stale: the environment is down.
func (c *Controller) liftStaleFence(ctx context.Context, claimRow store.BucketClaim, allocation *store.BucketAllocation) error {
	if allocation.FencedAt == nil || claimRow.EnvironmentID == nil {
		return nil
	}
	down, err := c.deps.DB.EnvironmentDown(ctx, *claimRow.EnvironmentID)
	if err != nil && !errors.Is(err, dbstore.ErrNotFound) {
		return err
	}
	if down {
		return nil
	}
	slog.Info("substrate: lifting a stale bucket fence", "bucket", allocation.BucketName)
	return c.unfence(ctx, allocation)
}

// serviceAllocation resolves a service's live bucket allocation.
func (c *Controller) serviceAllocation(ctx context.Context, environmentID uuid.UUID, serviceKey string) (*store.BucketAllocation, error) {
	claimRow, err := c.deps.DB.LiveServiceBucketClaim(ctx, environmentID, serviceKey)
	if err != nil {
		return nil, fmt.Errorf("substrate: resolve bucket claim for %s: %w", serviceKey, err)
	}
	allocation, err := c.deps.DB.LiveAllocation(ctx, claimRow.ID)
	if err != nil {
		return nil, fmt.Errorf("substrate: resolve bucket allocation for %s: %w", serviceKey, err)
	}
	return allocation, nil
}
