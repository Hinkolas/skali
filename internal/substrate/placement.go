package substrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// errWaiting keeps a claim pending with a visible reason instead of failing:
// level-triggered reconciliation retries toward the goal (there is no failed
// phase).
type errWaiting struct{ reason string }

func (e errWaiting) Error() string { return e.reason }

// requiredNodes maps a claim's availability intent to the database-capable
// node count that can honor it, mirroring the installer's tier derivation.
func requiredNodes(availability string) int {
	switch availability {
	case "synchronous":
		return 3
	case "asynchronous":
		return 2
	default:
		return 1
	}
}

// poolPlan describes a pool to create.
type poolPlan struct {
	engine        string
	major         int
	class         string
	name          string
	tier          layout.Tier
	environmentID uuid.UUID
	claimID       uuid.UUID
	storageBytes  int64
}

func sharedPoolName(engine string, major int) string {
	return poolPrefix(engine, major) + "-shared"
}

func poolPrefix(engine string, major int) string {
	prefix := "pg"
	if engine != "postgres" {
		prefix = engine
	}
	return fmt.Sprintf("%s%d", prefix, major)
}

// place selects or creates the pool a claim lands on and binds the claim to
// it. Placement policy (REWORK_V2 10.2, decisions 2026-07-29): dumb packing
// per isolation class on managed clusters; on local dev every isolation
// collapses onto the single dev pool, which is the documented parity
// boundary.
func (c *Controller) place(ctx context.Context, claim store.DatabaseClaim) (*store.DatabaseCluster, error) {
	engine, major := claim.Engine, int(claim.Major)
	if _, ok := cnpg.Lookup(engine, major); !ok {
		return nil, errWaiting{reason: fmt.Sprintf(
			"engine %s %d has no blessed image; supported majors: %v",
			engine, major, cnpg.SupportedMajors(engine))}
	}

	capable := c.capableNodes()
	if capable == 0 {
		return nil, errWaiting{reason: "no database-capable nodes observed yet"}
	}
	if required := requiredNodes(claim.Availability); required > capable {
		return nil, errWaiting{reason: fmt.Sprintf(
			"availability %s needs %d database-capable nodes, %d available",
			claim.Availability, required, capable)}
	}

	pool, err := c.selectPool(ctx, claim, capable)
	if err != nil {
		return nil, err
	}

	// A pool sized below the claim's availability intent cannot honor it;
	// tier upgrades are explicit operations, never side effects of a claim
	// arriving (REWORK_V2 14.3).
	if c.cfg.Managed && int(pool.Instances) < requiredNodes(claim.Availability) {
		return nil, errWaiting{reason: fmt.Sprintf(
			"pool %s runs %d instances, below availability %s; a tier upgrade is an explicit operation",
			pool.Name, pool.Instances, claim.Availability)}
	}

	if _, err := c.deps.DB.BindClaim(ctx, claim.ID, pool.ID); err != nil {
		return nil, err
	}
	return pool, nil
}

// capableNodes counts nodes eligible for database pools. Local dev clusters
// carry no capability labels by design; the single k3d node is the whole
// capacity and faking labels would misrepresent it.
func (c *Controller) capableNodes() int {
	if !c.cfg.Managed {
		return 1
	}
	return len(c.deps.Observed.CapableNodes(layout.CapabilityDatabase))
}

// selectPool finds the claim's live pool per packing policy or creates it.
func (c *Controller) selectPool(ctx context.Context, claim store.DatabaseClaim, capable int) (*store.DatabaseCluster, error) {
	engine, major := claim.Engine, int(claim.Major)

	// Local dev: one pool, every isolation class collapses onto it.
	isolation := claim.Isolation
	if !c.cfg.Managed {
		isolation = "shared"
	}

	switch isolation {
	case "shared":
		pool, err := c.deps.DB.LiveSharedCluster(ctx, engine, major)
		if err == nil {
			return pool, nil
		}
		if !errors.Is(err, dbstore.ErrNotFound) {
			return nil, err
		}
		// The shared pool sizes from the database-capable node count, the
		// same derivation as the bootstrap database (REWORK_V2 14.3).
		tier := layout.DeriveTier(capable)
		if !c.cfg.Managed {
			tier = layout.TierSingle
		}
		return c.createPool(ctx, poolPlan{
			engine: engine, major: major, class: dbstore.ClassShared,
			name: sharedPoolName(engine, major), tier: tier,
		})
	case "project":
		environmentID := uuid.Nil
		if claim.EnvironmentID != nil {
			environmentID = *claim.EnvironmentID
		}
		pool, err := c.deps.DB.LiveEnvironmentCluster(ctx, engine, major, environmentID)
		if err == nil {
			return pool, nil
		}
		if !errors.Is(err, dbstore.ErrNotFound) {
			return nil, err
		}
		// Environment and dedicated pools size from the creating claim's
		// availability intent; a later claim wanting more waits on an
		// explicit tier upgrade.
		return c.createPool(ctx, poolPlan{
			engine: engine, major: major, class: dbstore.ClassEnvironment,
			name:          poolPrefix(engine, major) + "-env-" + shortID(environmentID),
			tier:          tierForNodes(requiredNodes(claim.Availability)),
			environmentID: environmentID,
		})
	case "dedicated":
		pool, err := c.deps.DB.LiveDedicatedCluster(ctx, claim.ID)
		if err == nil {
			return pool, nil
		}
		if !errors.Is(err, dbstore.ErrNotFound) {
			return nil, err
		}
		return c.createPool(ctx, poolPlan{
			engine: engine, major: major, class: dbstore.ClassDedicated,
			name:         poolPrefix(engine, major) + "-ded-" + shortID(claim.ID),
			tier:         tierForNodes(requiredNodes(claim.Availability)),
			claimID:      claim.ID,
			storageBytes: claim.StorageBytes,
		})
	}
	return nil, fmt.Errorf("substrate: unknown isolation %q", claim.Isolation)
}

// createPool records the pool row; the CNPG objects follow through pool
// work.
func (c *Controller) createPool(ctx context.Context, plan poolPlan) (*store.DatabaseCluster, error) {
	image, ok := cnpg.Lookup(plan.engine, plan.major)
	if !ok {
		return nil, fmt.Errorf("substrate: no blessed image for %s %d", plan.engine, plan.major)
	}
	storage := plan.storageBytes
	floor := defaultPoolStorage
	if !c.cfg.Managed {
		floor = defaultDevPoolStorage
	}
	if storage < floor {
		storage = floor
	}
	instances := bundleTierInstances(plan.tier)
	pool, err := c.deps.DB.CreateCluster(ctx, dbstore.ClusterInput{
		Name:          plan.name,
		Engine:        plan.engine,
		Major:         plan.major,
		Class:         plan.class,
		EnvironmentID: plan.environmentID,
		ClaimID:       plan.claimID,
		Instances:     instances,
		StorageBytes:  storage,
		Image:         image.Ref,
	})
	if err != nil {
		return nil, err
	}
	return pool, nil
}

func shortID(id uuid.UUID) string {
	return strings.ReplaceAll(id.String(), "-", "")[:8]
}

// tierForNodes maps a node requirement back onto the availability tier.
func tierForNodes(nodes int) layout.Tier {
	switch {
	case nodes >= 3:
		return layout.TierSynchronous
	case nodes == 2:
		return layout.TierAsynchronous
	default:
		return layout.TierSingle
	}
}

// bundleTierInstances mirrors bundle.TierInstances without importing the
// installer bundle: one instance per tier level.
func bundleTierInstances(tier layout.Tier) int {
	switch tier {
	case layout.TierSynchronous:
		return 3
	case layout.TierAsynchronous:
		return 2
	default:
		return 1
	}
}
