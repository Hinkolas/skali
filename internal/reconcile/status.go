package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/revision"
)

// Status is one environment's topology and health projection, assembled
// from the database pointers and the in-memory observed store: never from a
// request-time Kubernetes call.
type Status struct {
	EnvironmentID uuid.UUID
	State         string // environment_targets.state: active, down, or releasing
	Target        *RevisionRef
	Active        *RevisionRef
	Observation   module.SourceStatus
	Services      []ServiceStatus
}

type RevisionRef struct {
	ID       uuid.UUID
	Checksum string
}

type ServiceStatus struct {
	Key         string
	Type        string
	Health      module.Health
	Diagnostics []module.Diagnostic
	Pods        []PodInfo
}

type PodInfo struct {
	Name     string
	Node     string
	Phase    string
	Ready    bool
	Restarts int32
	Reason   string
	Started  time.Time
}

// Status projects one environment. The single database read resolves the
// pointers; everything else comes from the observed store.
func (k *Kernel) Status(ctx context.Context, environmentID uuid.UUID) (*Status, error) {
	target, err := k.deps.Store.GetEnvironmentTarget(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("reconcile: get target: %w", err)
	}
	status := &Status{
		EnvironmentID: environmentID,
		State:         target.State,
		Observation:   k.deps.Observed.Source(),
	}
	var targetRevision *revision.Revision
	if target.TargetRevisionID != nil {
		row, err := k.deps.Store.GetRevisionByID(ctx, *target.TargetRevisionID)
		if err != nil {
			return nil, fmt.Errorf("reconcile: get target revision: %w", err)
		}
		status.Target = &RevisionRef{ID: row.ID, Checksum: row.Checksum}
		var decoded revision.Revision
		if err := json.Unmarshal(row.Document, &decoded); err != nil {
			return nil, fmt.Errorf("reconcile: decode target revision: %w", err)
		}
		targetRevision = &decoded
	}
	if target.ActiveRevisionID != nil {
		if status.Target != nil && *target.ActiveRevisionID == status.Target.ID {
			status.Active = status.Target
		} else {
			row, err := k.deps.Store.GetRevisionByID(ctx, *target.ActiveRevisionID)
			if err != nil {
				return nil, fmt.Errorf("reconcile: get active revision: %w", err)
			}
			status.Active = &RevisionRef{ID: row.ID, Checksum: row.Checksum}
		}
	}
	if targetRevision != nil {
		status.Services = k.evaluateServices(targetRevision, k.deps.Observed.Snapshot(environmentID))
	}
	return status, nil
}

// SubscribeStatus delivers invalidation nudges for one environment; the
// subscriber re-reads Status on every tick.
func (k *Kernel) SubscribeStatus(environmentID uuid.UUID) (<-chan observe.Invalidation, func()) {
	return k.deps.Observed.Subscribe(environmentID)
}

// evaluateServices projects per-service health through the registered
// modules; a missing module (the application module ships in R3) reports
// unknown with a diagnostic rather than guessing.
func (k *Kernel) evaluateServices(rev *revision.Revision, snapshot observe.Snapshot) []ServiceStatus {
	keys := make([]string, 0, len(rev.Definition.Applications))
	for key := range rev.Definition.Applications {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	statuses := make([]ServiceStatus, 0, len(keys))
	for _, key := range keys {
		status := ServiceStatus{Key: key, Type: "application", Pods: podsFor(snapshot, key)}
		mod, registered := k.deps.Registry.Get("application")
		if !registered {
			status.Health = module.HealthUnknown
			status.Diagnostics = []module.Diagnostic{{
				Severity: "warning", Code: "module-unavailable",
				Message: "no module registered for service type application",
			}}
			statuses = append(statuses, status)
			continue
		}
		service, err := mod.Decode(rev.Definition, key)
		if err != nil {
			status.Health = module.HealthUnknown
			status.Diagnostics = []module.Diagnostic{{
				Severity: "error", Code: "decode-failed",
				Message: "decoding the service failed: " + err.Error(),
			}}
			statuses = append(statuses, status)
			continue
		}
		evaluation := service.Evaluate(snapshot.ForService(key))
		status.Health = evaluation.Health
		status.Diagnostics = evaluation.Diagnostics
		statuses = append(statuses, status)
	}
	return statuses
}

func podsFor(snapshot observe.Snapshot, service string) []PodInfo {
	var pods []PodInfo
	for _, obj := range snapshot.Objects {
		if obj.Kind != module.KindPod || obj.Service != service || obj.Pod == nil {
			continue
		}
		pods = append(pods, PodInfo{
			Name:     obj.Name,
			Node:     obj.Pod.Node,
			Phase:    obj.Pod.Phase,
			Ready:    obj.Pod.Ready,
			Restarts: obj.Pod.Restarts,
			Reason:   obj.Pod.Reason,
			Started:  obj.Pod.Started,
		})
	}
	return pods
}
