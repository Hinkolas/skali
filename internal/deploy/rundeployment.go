package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/plan"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/utils"
)

// RunDeployment is what a deployment, rollback, or restart run changes,
// stored on the run so history views can say what each rollout did
// without recomputing plans. Like the rest of the journal it explains and
// never drives: nothing reads it back to decide anything.
type RunDeployment struct {
	// Revision is the checksum of the revision the run moves the target
	// to. A deployment knows it once its revision is stored; a restart
	// moves no target and has none.
	Revision string `json:"revision,omitempty"`
	// FromEnvironment names the environment a promotion copied.
	FromEnvironment string `json:"from_environment,omitempty"`
	// Changes and Values are the plan. A deployment's is against the
	// environment's active revision: what the deployer confirmed when it
	// opened, replaced at promotion by the plan of the revision actually
	// stored. A rollback's is against the target it moves away from.
	Changes []plan.Change    `json:"changes,omitempty"`
	Values  []RunValueChange `json:"values,omitempty"`
	// Restarts lists the applications the run restarts, by dotted name:
	// the one a single-application restart addresses, every one for an
	// environment-wide restart or a forced deployment.
	Restarts []string `json:"restarts,omitempty"`
}

// RunValueChange is one value change of the plan with the services that
// use the value, by dotted name. A new version rolls the pods of the
// applications whose environment references it (their pod templates carry
// the values identity) and moves the routes whose domain does, though
// their own plan rows may say nothing at all.
type RunValueChange struct {
	plan.ValueChange
	Services []string `json:"services,omitempty"`
}

// setPlan records a plan whose candidate is rev, the revision the run
// moves to; its services are the ones a changed value reaches.
func (d *RunDeployment) setPlan(document *plan.Plan, rev *revision.Revision) {
	d.Changes, d.Values = nil, nil
	if document == nil {
		return
	}
	d.Changes = document.Changes
	users := valueUsers(rev)
	for _, value := range document.Values {
		d.Values = append(d.Values, RunValueChange{ValueChange: value, Services: users[value.Name]})
	}
}

// valueUsers maps each project value to the services of rev that use it:
// applications through their environment or a route's domain or aliases,
// buckets through their route domain. Names come sorted per value.
func valueUsers(rev *revision.Revision) map[string][]string {
	users := map[string][]string{}
	use := func(service string, expressions []compiler.Expression) {
		seen := map[string]bool{}
		for _, expression := range expressions {
			for _, part := range expression.Parts {
				if part.Kind == "project_variable" && !seen[part.Name] {
					seen[part.Name] = true
					users[part.Name] = append(users[part.Name], service)
				}
			}
		}
	}
	definition := rev.Definition
	for _, key := range utils.SortedKeys(definition.Applications) {
		application := definition.Applications[key]
		expressions := slices.Collect(maps.Values(application.Environment))
		for _, route := range application.Routes {
			expressions = append(expressions, route.Domain)
			expressions = append(expressions, route.Aliases...)
		}
		use("applications."+key, expressions)
	}
	for _, key := range utils.SortedKeys(definition.Buckets) {
		if route := definition.Buckets[key].Route; route != nil {
			use("buckets."+key, []compiler.Expression{route.Domain})
		}
	}
	return users
}

func (d *RunDeployment) encode() (json.RawMessage, error) {
	data, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("deploy: encode run deployment: %w", err)
	}
	return data, nil
}

// restartedApplications lists the applications a restart of the whole
// revision recreates: every one with a workload, so not the host-run
// intercepts, which carry no artifact.
func restartedApplications(rev *revision.Revision) []string {
	var names []string
	for key := range rev.Definition.Applications {
		if _, ok := rev.Artifacts[key]; ok {
			names = append(names, "applications."+key)
		}
	}
	sort.Strings(names)
	return names
}

// activeRevision reads the revision the environment runs now; nil before
// its first activation.
func (s *Service) activeRevision(ctx context.Context, environmentID uuid.UUID) (*revision.Revision, error) {
	target, err := s.st.GetEnvironmentTarget(ctx, environmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("deploy: get target: %w", err)
	}
	if target.ActiveRevisionID == nil {
		return nil, nil
	}
	return s.GetRevision(ctx, *target.ActiveRevisionID)
}

// revisionOrNil reads a revision by an optional id.
func (s *Service) revisionOrNil(ctx context.Context, id *uuid.UUID) (*revision.Revision, error) {
	if id == nil {
		return nil, nil
	}
	return s.GetRevision(ctx, *id)
}

// recordPromotion replaces a promoted deployment run's plan with the one
// of the revision it stored against the one it replaced (Prepared.
// Replaced), which carries the artifacts' real digests where the open
// only knew a new artifact was coming; one identical to the running
// artifact is no change at all. What the open recorded beyond the plan
// (the promotion source) stays.
func (s *Service) recordPromotion(ctx context.Context, jsvc *journal.Service, runID uuid.UUID,
	prepared *Prepared, restart bool) error {
	replaced, err := s.revisionOrNil(ctx, prepared.Replaced)
	if err != nil {
		return err
	}
	var document RunDeployment
	run, err := s.st.GetRunByID(ctx, runID)
	if err != nil {
		return fmt.Errorf("deploy: get run: %w", err)
	}
	if len(run.Deployment) > 0 {
		if err := json.Unmarshal(run.Deployment, &document); err != nil {
			return fmt.Errorf("deploy: decode run deployment: %w", err)
		}
	}
	planned := plan.Diff(replaced, prepared.Revision)
	planned.Prune(prepared.Pruned)
	document.setPlan(planned, prepared.Revision)
	document.Revision = prepared.Revision.Checksum
	document.Restarts = nil
	if restart {
		document.Restarts = restartedApplications(prepared.Revision)
	}
	encoded, err := document.encode()
	if err != nil {
		return err
	}
	return jsvc.SetRunDeployment(ctx, runID, encoded)
}

// recordRollback records what a rollback to rev reverses: its plan
// against the target it moves away from rather than the active revision,
// since the usual rollback undoes a rollout that failed before it
// activated, and its plan against the still active revision would be
// empty.
func (s *Service) recordRollback(ctx context.Context, jsvc *journal.Service, runID uuid.UUID,
	targetID *uuid.UUID, rev *revision.Revision) error {
	target, err := s.revisionOrNil(ctx, targetID)
	if err != nil {
		return err
	}
	document := RunDeployment{Revision: rev.Checksum}
	document.setPlan(plan.Diff(target, rev), rev)
	encoded, err := document.encode()
	if err != nil {
		return err
	}
	return jsvc.SetRunDeployment(ctx, runID, encoded)
}
