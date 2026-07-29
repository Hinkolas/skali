package substrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/store"
)

// Ensure implements reconcile.ClaimManager: it records the revision's
// database claims, enqueues their reconciliation, and reports readiness.
// The kernel never sees claim mechanics; the substrate never sees the
// deployment state machine.
func (c *Controller) Ensure(ctx context.Context, in reconcile.ClaimEnsureInput) ([]reconcile.ClaimState, error) {
	keys := make([]string, 0, len(in.Revision.Definition.Databases))
	for key := range in.Revision.Definition.Databases {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	states := make([]reconcile.ClaimState, 0, len(keys))
	for _, key := range keys {
		database := in.Revision.Definition.Databases[key]
		dotted := "databases." + key
		major, err := strconv.Atoi(strings.TrimSpace(database.Version))
		if err != nil {
			states = append(states, reconcile.ClaimState{Service: dotted,
				Waiting: fmt.Sprintf("unsupported %s version %q", database.Engine, database.Version)})
			continue
		}
		owner := dbstore.ServiceOwner(in.ProjectID, in.EnvironmentID,
			in.Revision.Project, in.Revision.Environment, key)
		row, err := c.deps.DB.EnsureClaim(ctx, owner, dbstore.ClaimSpec{
			Engine:       database.Engine,
			Major:        major,
			Isolation:    database.Isolation,
			Availability: database.Availability,
			StorageBytes: database.StorageBytes,
			Extensions:   database.Extensions,
			PITRSeconds:  database.PointInTimeRecoverySec,
		})
		if errors.Is(err, dbstore.ErrSpecConflict) {
			states = append(states, reconcile.ClaimState{Service: dotted,
				Waiting: "the requested engine, version, or isolation differs from the live database; replacing it is a destructive change"})
			continue
		}
		if err != nil {
			return nil, err
		}
		c.EnqueueClaim(row.ID)
		c.publishClaim(*row)

		state := reconcile.ClaimState{Service: dotted,
			Provisioned: claim.Phase(row.Phase) == claim.PhaseProvisioned}
		if !state.Provisioned {
			state.Waiting = c.WaitingReason(row.ID)
			if state.Waiting == "" {
				state.Waiting = defaultWait(claim.Phase(row.Phase))
			}
		}
		states = append(states, state)
	}

	bucketStates, err := c.ensureBucketClaims(ctx, in)
	if err != nil {
		return nil, err
	}
	states = append(states, bucketStates...)

	// Claims whose service left the promoted revision release now: the
	// destructive gate already ran at deploy open, and the promoted
	// revision is the persisted decision.
	live, err := c.deps.DB.ListEnvironmentClaims(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	for _, row := range live {
		if _, kept := in.Revision.Definition.Databases[row.ServiceKey]; kept {
			continue
		}
		released, err := c.deps.DB.ReleaseClaim(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		c.EnqueueClaim(row.ID)
		c.publishClaim(*released)
	}
	liveBuckets, err := c.deps.DB.ListEnvironmentBucketClaims(ctx, in.EnvironmentID)
	if err != nil {
		return nil, err
	}
	for _, row := range liveBuckets {
		if _, kept := in.Revision.Definition.Buckets[row.ServiceKey]; kept {
			continue
		}
		released, err := c.deps.DB.ReleaseBucketClaim(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		c.EnqueueBucketClaim(row.ID)
		c.publishBucketClaim(*released)
	}
	return states, nil
}

// ensureBucketClaims records the revision's bucket claims, mirroring the
// database loop above.
func (c *Controller) ensureBucketClaims(ctx context.Context, in reconcile.ClaimEnsureInput) ([]reconcile.ClaimState, error) {
	keys := make([]string, 0, len(in.Revision.Definition.Buckets))
	for key := range in.Revision.Definition.Buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	states := make([]reconcile.ClaimState, 0, len(keys))
	for _, key := range keys {
		bucket := in.Revision.Definition.Buckets[key]
		dotted := "buckets." + key
		owner := dbstore.ServiceOwner(in.ProjectID, in.EnvironmentID,
			in.Revision.Project, in.Revision.Environment, key)
		row, err := c.deps.DB.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
			Visibility:                   bucket.Visibility,
			StorageQuotaBytes:            bucket.StorageQuotaBytes,
			ObjectQuota:                  int64(bucket.ObjectQuota),
			MaxObjectBytes:               bucket.MaxObjectSizeBytes,
			Versioning:                   bucket.Versioning,
			AbortUploadsAfterSeconds:     bucket.AbortIncompleteUploadsAfterSeconds,
			ExpireNoncurrentAfterSeconds: bucket.ExpireNoncurrentVersionsAfterSec,
		})
		if errors.Is(err, dbstore.ErrSpecConflict) {
			states = append(states, reconcile.ClaimState{Service: dotted,
				Waiting: "the requested visibility or versioning differs from the live bucket; replacing it is a destructive change"})
			continue
		}
		if err != nil {
			return nil, err
		}
		c.EnqueueBucketClaim(row.ID)
		c.publishBucketClaim(*row)

		state := reconcile.ClaimState{Service: dotted,
			Provisioned: claim.Phase(row.Phase) == claim.PhaseProvisioned}
		if !state.Provisioned {
			state.Waiting = c.WaitingReason(row.ID)
			if state.Waiting == "" {
				state.Waiting = bucketWait(claim.Phase(row.Phase))
			}
		}
		states = append(states, state)
	}
	return states, nil
}

func defaultWait(phase claim.Phase) string {
	switch phase {
	case claim.PhaseBound:
		return "provisioning the database tenant"
	case claim.PhaseReleasing, claim.PhaseReleased:
		return "the database is being released"
	default:
		return "waiting for placement"
	}
}

// publishClaim upserts the claim's provider observation (REWORK_V2 7.4) so
// health evaluation stays pure over observed input; released claims leave
// the store.
func (c *Controller) publishClaim(row store.DatabaseClaim) {
	if c.deps.Observed == nil || row.OwnerKind != dbstore.OwnerService || row.EnvironmentID == nil {
		return
	}
	if claim.Phase(row.Phase) == claim.PhaseReleased {
		c.deps.Observed.Remove(observe.ClaimRef(row.ID))
		return
	}
	c.deps.Observed.Upsert(observe.ClaimObject(*row.EnvironmentID,
		"databases."+row.ServiceKey, row.ID, module.ClaimStatus{
			Phase:   row.Phase,
			Waiting: c.WaitingReason(row.ID),
		}))
}

// publishLiveClaims rebuilds every claim projection from rows on boot.
func (c *Controller) publishLiveClaims(ctx context.Context) error {
	claims, err := c.deps.DB.ListLiveClaims(ctx)
	if err != nil {
		return err
	}
	for _, row := range claims {
		c.publishClaim(row)
	}
	return nil
}
