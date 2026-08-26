package installer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
)

// TierPlan states what an availability-tier change would do, computed
// purely from a gathered status so planning stays read-only.
type TierPlan struct {
	Deployed  layout.Tier
	Available layout.Tier
	// DatabaseNodes lists the database-capable nodes deriving Available.
	DatabaseNodes []string
	// Instances is the target skali-db instance count.
	Instances int
	// Downgrade means the available tier is lower than the deployed one
	// (a database node left); allowed, with an explicit warning.
	Downgrade bool
}

// Nothing reports whether deployed and available tiers already agree.
func (p TierPlan) Nothing() bool {
	return p.Deployed == p.Available
}

// PlanTier derives the tier plan from a gathered status. Every refusal
// names the command that resolves it. The bundle-current gate is load
// bearing: tier apply re-stamps the bundle hash, which is only truthful
// when the pre-state was a completed converge of this same version.
func PlanTier(status *Status) (TierPlan, error) {
	var plan TierPlan
	record := status.Host.Record
	if record == nil || record.Node.Role != layout.RoleServer {
		return plan, errors.New("tier changes run on a server node")
	}
	if !status.Initialized {
		return plan, errors.New("the bundle was never initialized; run skali cluster init first")
	}
	if !status.ClusterReachable {
		return plan, errors.New("the kubernetes api is unreachable; inspect with skali cluster diagnose")
	}
	if status.InitOwner != "" {
		return plan, fmt.Errorf("the bundle is maintained on %s; run skali cluster tier there", status.InitOwner)
	}
	if !status.BundleCurrent {
		return plan, errors.New("the bundle is not current on this host; run skali cluster upgrade first " +
			"(it converges at the available tier)")
	}
	if status.DeployedTier == "" {
		return plan, errors.New("the bootstrap database was not found; inspect with skali cluster diagnose")
	}
	plan.Deployed = status.DeployedTier
	plan.Available = status.AvailableTier
	plan.DatabaseNodes = status.DatabaseNodes
	plan.Instances = layout.TierInstances(status.AvailableTier)
	plan.Downgrade = plan.Instances < layout.TierInstances(status.DeployedTier)
	return plan, nil
}

// TierOptions parameterize ApplyTier.
type TierOptions struct {
	// Client overrides the kube client; nil derives it from the runner.
	Client   *kube.Client
	Progress Progress
}

// ApplyTier scales the bootstrap database to the planned tier: the
// namespace stage is applied plain first (clearing the bundle-hash stamp,
// so a half-applied change never looks current), then the database stage
// at the new tier, then the readiness wait, then the stamp with the
// new-tier hash. Deliberately NOT a full Init: the record stays untouched
// (no version bump, no SaveRecord), the display shows exactly these two
// steps, and the database stage is the only
// tier-dependent render, so the post-scale cluster equals a full converge
// at the new tier.
func ApplyTier(ctx context.Context, runner host.Runner, record *Record, plan TierPlan, opts TierOptions) error {
	progress := opts.Progress
	if progress == nil {
		progress = silentProgress{}
	}
	client := opts.Client
	if client == nil {
		var err error
		client, err = KubeClient(ctx, runner)
		if err != nil {
			return err
		}
	}

	profile, _, err := LiveProfile(ctx, client, runner, record)
	if err != nil {
		return err
	}
	if profile.Production.DatabaseTier != plan.Available {
		return fmt.Errorf("the cluster changed since planning (derived tier is now %s, planned %s); re-run",
			profile.Production.DatabaseTier, plan.Available)
	}
	objects, err := bundle.Render(profile)
	if err != nil {
		return err
	}
	applier := &bundle.Applier{Client: client, WaitTimeout: 10 * time.Minute}

	progress.Start(fmt.Sprintf("Scale bootstrap database to %d instance(s) (%s)",
		plan.Instances, tierDescription(plan.Available)))
	if err := applier.ApplyObjects(ctx, objects.Namespace); err != nil {
		return err
	}
	// The database names a PriorityClass; a re-tier of a cluster converged
	// before the classes existed must not leave it Pending.
	if err := applier.ApplyObjects(ctx, objects.Priority); err != nil {
		return err
	}
	if err := applier.ApplyObjects(ctx, objects.Database); err != nil {
		return err
	}
	progress.Done("")

	progress.Start("Verify replication state")
	if err := applier.WaitClusterReady(ctx, bundle.Namespace, "skali-db", plan.Instances); err != nil {
		return fmt.Errorf("%w; the change is applied and re-running skali cluster tier resumes the wait, "+
			"inspect with: skali cluster diagnose", err)
	}
	progress.Done("")

	return bundle.StampHash(ctx, client, profile)
}

func tierDescription(tier layout.Tier) string {
	switch tier {
	case layout.TierSynchronous:
		return "quorum any 1 of 2"
	case layout.TierAsynchronous:
		return "asynchronous replication"
	default:
		return "single instance"
	}
}
