package substrate

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// patchWait paces a pool whose patch update waits for the installation's
// pools to settle: the one rolling before it, or one that is unhealthy.
const patchWait = 30 * time.Second

// patchPool moves a pool onto its major's current catalog image when the
// catalog lists the image the pool runs as a reviewed patch source, and
// returns the row to apply. CNPG rolls the change in place: replicas
// first, then the primary restarts, so a single-instance pool is briefly
// unavailable.
//
// Pools move one at a time, and only while every live pool, this one
// included and those being released excepted, is settled (healthy, with
// each member running its row's image). An update that does not settle,
// an image that cannot be pulled or does not start, therefore stops the
// rollout at one pool instead of reaching them all.
// A pool on an image the catalog does not list keeps it.
func (c *Controller) patchPool(ctx context.Context, pool store.DatabaseCluster) (store.DatabaseCluster, time.Duration, error) {
	image, ok := cnpg.Lookup(pool.Engine, int(pool.Major))
	if !ok || image.Ref == pool.Image {
		return pool, 0, nil
	}
	if !image.Patches(pool.Image) {
		c.noteUnpatched(pool, image.Ref)
		return pool, 0, nil
	}

	c.patchMu.Lock()
	defer c.patchMu.Unlock()
	pools, err := c.deps.DB.ListLiveClusters(ctx)
	if err != nil {
		return pool, 0, fmt.Errorf("substrate: list pools for a patch update: %w", err)
	}
	for _, other := range pools {
		if other.State == dbstore.StateReleasing {
			// Going away: its teardown is no sign of the update's health.
			continue
		}
		settled, reason, err := c.poolSettled(ctx, other)
		if err != nil {
			return pool, 0, err
		}
		if !settled {
			slog.Info("substrate: pool patch update waits", "pool", pool.Name, "to", image.Ref, "reason", reason)
			return pool, patchWait, nil
		}
	}
	moved, err := c.deps.DB.SetClusterImage(ctx, pool.ID, pool.Image, image.Ref)
	if err != nil {
		return pool, 0, err
	}
	if !moved {
		// Another pass moved or released it first; the next pass works
		// from the row as it is now.
		return pool, requeueWait, nil
	}
	slog.Info("substrate: pool patch update", "pool", pool.Name, "from", pool.Image, "to", image.Ref)
	pool.Image = image.Ref
	return pool, 0, nil
}

// poolSettled reports whether a pool runs the image its row records
// throughout (cnpg.Settled), with the reason when it does not.
func (c *Controller) poolSettled(ctx context.Context, pool store.DatabaseCluster) (bool, string, error) {
	object, err := c.deps.Cluster.GetObject(ctx, cnpg.ClusterGVR, Namespace, pool.Name)
	if apierrors.IsNotFound(err) {
		return false, fmt.Sprintf("pool %s is not created yet", pool.Name), nil
	}
	if err != nil {
		return false, "", fmt.Errorf("substrate: read pool %s: %w", pool.Name, err)
	}
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	members, err := c.PoolMembers(ctx, pool.Name)
	if err != nil {
		return false, "", err
	}
	if settled, reason := cnpg.Settled(phase, members, pool.Image); !settled {
		return false, fmt.Sprintf("pool %s: %s", pool.Name, reason), nil
	}
	return true, "", nil
}

// noteUnpatched logs, once per process and pool image, a pool the catalog
// has a newer image for but no reviewed transition from the one it runs.
func (c *Controller) noteUnpatched(pool store.DatabaseCluster, current string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unpatched[pool.ID] == pool.Image {
		return
	}
	c.unpatched[pool.ID] = pool.Image
	slog.Warn("substrate: pool keeps its image; no reviewed patch update from it",
		"pool", pool.Name, "image", pool.Image, "current", current)
}
