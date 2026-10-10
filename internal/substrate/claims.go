package substrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
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
// database claims, enqueues the reconciliation of new, changed, and
// unsettled ones, and reports readiness. A settled claim is repaired on the
// substrate's own cadence (scheduleRepair), not on every environment pass,
// unless the pass asks for every claim (ClaimEnsureInput.Repair).
// The kernel never sees claim mechanics; the substrate never sees the
// deployment state machine. One read of each claim list, the one behind
// the pass's Outputs when it hands it on, serves the comparisons and the
// release of the claims the revision dropped, so a pass that changes no
// claim reads nothing more and writes nothing.
func (c *Controller) Ensure(ctx context.Context, in reconcile.ClaimEnsureInput) ([]reconcile.ClaimState, error) {
	read, ok := in.Live.(*liveClaims)
	if !ok {
		var err error
		if read, err = c.readLiveClaims(ctx, in.EnvironmentID); err != nil {
			return nil, err
		}
	}
	live, liveBuckets := read.claims()
	liveByKey := make(map[string]*store.DatabaseClaim, len(live))
	for i := range live {
		liveByKey[live[i].ServiceKey] = &live[i]
	}

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
		// A provisioned claim whose extension list is about to change
		// must stop reporting ready until the change is applied; the
		// pre-image is the only place that difference is visible. The
		// marker is set only once the fold succeeded, so a spec conflict
		// never leaves a request behind that no pass could ever apply.
		desiredExtensions := dbstore.MarshalExtensions(database.Extensions)
		current := liveByKey[key]
		extensionsChanged := current != nil && claim.Phase(current.Phase) == claim.PhaseProvisioned &&
			!bytes.Equal(current.Extensions, desiredExtensions)
		row, changed, err := c.deps.DB.EnsureClaimFrom(ctx, owner, dbstore.ClaimSpec{
			Engine:       database.Engine,
			Major:        major,
			Isolation:    database.Isolation,
			Availability: database.Availability,
			StorageBytes: database.StorageBytes,
			Extensions:   database.Extensions,
			PITRSeconds:  database.PointInTimeRecoverySec,
		}, current)
		if errors.Is(err, dbstore.ErrSpecConflict) {
			states = append(states, reconcile.ClaimState{Service: dotted,
				Waiting: "the requested engine, version, or isolation differs from the live database; replacing it is a destructive change"})
			continue
		}
		if err != nil {
			return nil, err
		}
		if extensionsChanged {
			c.markExtensionsPending(row.ID, desiredExtensions)
		}
		switch {
		case changed || claim.Phase(row.Phase) != claim.PhaseProvisioned || c.extensionsPending(row.ID):
			c.EnqueueClaim(row.ID, reasonEnsure)
		case in.Repair:
			c.EnqueueClaim(row.ID, reasonCheck)
		}
		c.publishClaim(*row)

		state := reconcile.ClaimState{Service: dotted,
			Provisioned: claim.Phase(row.Phase) == claim.PhaseProvisioned}
		if state.Provisioned && c.extensionsPending(row.ID) {
			state.Provisioned = false
			state.Waiting = extensionsPendingReason
		}
		if !state.Provisioned && state.Waiting == "" {
			state.Waiting = c.WaitingReason(row.ID)
			if state.Waiting == "" {
				state.Waiting = defaultWait(claim.Phase(row.Phase))
			}
		}
		states = append(states, state)
	}

	bucketStates, err := c.ensureBucketClaims(ctx, in, liveBuckets)
	if err != nil {
		return nil, err
	}
	states = append(states, bucketStates...)

	// Claims whose service left the promoted revision release now: the
	// destructive gate already ran at deploy open, and the promoted
	// revision is the persisted decision.
	for _, row := range live {
		if _, kept := in.Revision.Definition.Databases[row.ServiceKey]; kept {
			continue
		}
		released, err := c.deps.DB.ReleaseClaim(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		c.EnqueueClaim(row.ID, reasonRelease)
		c.publishClaim(*released)
	}
	for _, row := range liveBuckets {
		if _, kept := in.Revision.Definition.Buckets[row.ServiceKey]; kept {
			continue
		}
		released, err := c.deps.DB.ReleaseBucketClaim(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		c.EnqueueBucketClaim(row.ID, reasonRelease)
		c.publishBucketClaim(*released)
	}
	return states, nil
}

// ensureBucketClaims records the revision's bucket claims against the live
// ones, mirroring the database loop above.
func (c *Controller) ensureBucketClaims(ctx context.Context, in reconcile.ClaimEnsureInput, live []store.BucketClaim) ([]reconcile.ClaimState, error) {
	liveByKey := make(map[string]*store.BucketClaim, len(live))
	for i := range live {
		liveByKey[live[i].ServiceKey] = &live[i]
	}
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
		var err error
		var corsSpec []byte
		if bucket.CORS != nil {
			corsSpec, err = json.Marshal(bucket.CORS)
			if err != nil {
				return nil, fmt.Errorf("substrate: encode cors for %s: %w", dotted, err)
			}
		}
		var routeSpec []byte
		if route, ok := in.BucketRoutes[key]; ok {
			routeSpec, err = json.Marshal(route)
			if err != nil {
				return nil, fmt.Errorf("substrate: encode route for %s: %w", dotted, err)
			}
		}
		row, changed, err := c.deps.DB.EnsureBucketClaimFrom(ctx, owner, dbstore.BucketSpec{
			Visibility:                   bucket.Visibility,
			StorageQuotaBytes:            bucket.StorageQuotaBytes,
			ObjectQuota:                  int64(bucket.ObjectQuota),
			MaxObjectBytes:               bucket.MaxObjectSizeBytes,
			Versioning:                   bucket.Versioning,
			AbortUploadsAfterSeconds:     bucket.AbortIncompleteUploadsAfterSeconds,
			ExpireNoncurrentAfterSeconds: bucket.ExpireNoncurrentVersionsAfterSec,
			CORS:                         corsSpec,
			Route:                        routeSpec,
		}, liveByKey[key])
		if errors.Is(err, dbstore.ErrSpecConflict) {
			states = append(states, reconcile.ClaimState{Service: dotted,
				Waiting: "the requested visibility or versioning differs from the live bucket; replacing it is a destructive change"})
			continue
		}
		if err != nil {
			return nil, err
		}
		switch {
		case changed || claim.Phase(row.Phase) != claim.PhaseProvisioned:
			c.EnqueueBucketClaim(row.ID, reasonEnsure)
		case in.Repair:
			c.EnqueueBucketClaim(row.ID, reasonCheck)
		}
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

// publishClaim upserts the claim's provider observation so
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

// Outputs implements reconcile.ClaimManager in two reads: the
// environment's live service claims with their live tenants, and with their
// live allocations.
//
// Generations get, per provisioned claim ("databases.data",
// "buckets.files"), a short non-secret identity of its connection outputs:
// the endpoint(s) and the credential version. The kernel folds it into the
// pod-template identity of every application referencing the service, so a
// republished endpoint or a rotated credential rolls exactly those
// consumers; the plaintext credentials never enter it.
//
// BucketNames get the store bucket name of every live bucket claim with an
// allocation, keyed by bucket key. Phase does not matter there: the name is
// fixed the moment the allocation is recorded, and a route may render
// before the bucket finishes provisioning.
//
// The read behind the outputs travels on as their Live value, so the
// pass's Ensure compares the revision with the same claims instead of
// reading them again.
func (c *Controller) Outputs(ctx context.Context, environmentID uuid.UUID) (reconcile.ClaimOutputs, error) {
	read, err := c.readLiveClaims(ctx, environmentID)
	if err != nil {
		return reconcile.ClaimOutputs{}, err
	}
	outputs := reconcile.ClaimOutputs{Generations: map[string]string{}, BucketNames: map[string]string{}, Live: read}
	for _, row := range read.databases {
		if claim.Phase(row.DatabaseClaim.Phase) != claim.PhaseProvisioned || row.TenantHost == nil {
			continue
		}
		outputs.Generations["databases."+row.DatabaseClaim.ServiceKey] = outputGeneration(
			"host="+*row.TenantHost, fmt.Sprintf("port=%d", *row.TenantPort),
			fmt.Sprintf("credential=v%d", *row.TenantCredentialVersion))
	}
	for _, row := range read.buckets {
		if row.AllocationBucketName == nil {
			continue
		}
		outputs.BucketNames[row.BucketClaim.ServiceKey] = *row.AllocationBucketName
		if claim.Phase(row.BucketClaim.Phase) != claim.PhaseProvisioned {
			continue
		}
		// The output version advances only once the mirror Secret holds
		// new values (publishBucketOutputs), so consumers never roll ahead
		// of what they read.
		outputs.Generations["buckets."+row.BucketClaim.ServiceKey] = outputGeneration(
			"endpoint="+*row.AllocationEndpoint, "internal_endpoint="+InternalBucketEndpoint(),
			fmt.Sprintf("credential=v%d", *row.AllocationCredentialVersion),
			fmt.Sprintf("outputs=v%d", *row.AllocationOutputVersion))
	}
	return outputs, nil
}

// liveClaims is one read of an environment's live service claims, each
// with its live tenant or allocation when it has one.
type liveClaims struct {
	databases []store.ListLiveDatabaseClaimOutputsByEnvironmentRow
	buckets   []store.ListLiveBucketClaimOutputsByEnvironmentRow
}

func (c *Controller) readLiveClaims(ctx context.Context, environmentID uuid.UUID) (*liveClaims, error) {
	databases, err := c.deps.DB.EnvironmentClaimOutputs(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	buckets, err := c.deps.DB.EnvironmentBucketClaimOutputs(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	return &liveClaims{databases: databases, buckets: buckets}, nil
}

// claims returns the claim rows of the read.
func (l *liveClaims) claims() ([]store.DatabaseClaim, []store.BucketClaim) {
	databases := make([]store.DatabaseClaim, len(l.databases))
	for i, row := range l.databases {
		databases[i] = row.DatabaseClaim
	}
	buckets := make([]store.BucketClaim, len(l.buckets))
	for i, row := range l.buckets {
		buckets[i] = row.BucketClaim
	}
	return databases, buckets
}

// outputGeneration hashes the non-secret facts of a service's outputs into
// a short stable identity.
func outputGeneration(facts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(facts, "\n")))
	return hex.EncodeToString(sum[:8])
}
