package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/mirror"
	"github.com/Hinkolas/skali/internal/store"
)

// Workload sentinels, mapped by the REST layer.
var (
	ErrWorkloadNotFound = errors.New("reconcile: workload not found")
	ErrWorkloadDeleting = errors.New("reconcile: workload is being deleted")
	ErrNameTaken        = errors.New("reconcile: workload name already in use")
	ErrInvalidWorkload  = errors.New("reconcile: invalid workload")
)

// Desired states a user can ask for; 'deleting' is entered through Delete.
const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"
	DesiredDeleting = "deleting"
)

// Service is the workload business layer the REST handlers drive: validate,
// persist desired state, poke the reconciler. It never touches nodes — the
// reconciler owns all engine effects, so every mutation returns fast.
type Service struct {
	st   *store.Store
	poke func()
}

// NewService wires the service; poke wakes the reconciler after mutations
// (nil for harnesses without one).
func NewService(st *store.Store, poke func()) *Service {
	if poke == nil {
		poke = func() {}
	}
	return &Service{st: st, poke: poke}
}

// WorkloadInput is the create surface. Zero Replicas means zero replicas —
// callers wanting a default apply it at the edge.
type WorkloadInput struct {
	Name         string
	Kind         string
	Image        string
	Replicas     int
	DesiredState string
	Constraints  Constraints
	Spec         Spec
}

// WorkloadUpdate is the partial update surface: nil fields keep their
// current value. Name is immutable (it stems container names).
type WorkloadUpdate struct {
	Image        *string
	Replicas     *int
	DesiredState *string
	Constraints  *Constraints
	Spec         *Spec
}

func (s *Service) Create(ctx context.Context, in WorkloadInput) (store.Workload, error) {
	if in.DesiredState == "" {
		in.DesiredState = DesiredRunning
	}
	if err := validateInput(in); err != nil {
		return store.Workload{}, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return store.Workload{}, err
	}
	specJSON, constraintsJSON, err := encodeSpec(in.Spec, in.Constraints)
	if err != nil {
		return store.Workload{}, err
	}
	row, err := s.st.InsertWorkload(ctx, store.InsertWorkloadParams{
		ID:           id,
		Name:         in.Name,
		Kind:         in.Kind,
		DesiredState: in.DesiredState,
		Replicas:     int32(in.Replicas),
		Constraints:  constraintsJSON,
		Image:        in.Image,
		Spec:         specJSON,
	})
	if isUniqueViolation(err) {
		return store.Workload{}, fmt.Errorf("%w: %s", ErrNameTaken, in.Name)
	}
	if err != nil {
		return store.Workload{}, err
	}
	s.poke()
	return row, nil
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, up WorkloadUpdate) (store.Workload, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return store.Workload{}, err
	}
	if current.DesiredState == DesiredDeleting {
		return store.Workload{}, ErrWorkloadDeleting
	}
	w, err := decodeWorkload(current)
	if err != nil {
		return store.Workload{}, err
	}

	in := WorkloadInput{
		Name:         w.Name,
		Kind:         w.Kind,
		Image:        w.Image,
		Replicas:     int(w.Replicas),
		DesiredState: w.DesiredState,
		Constraints:  w.constraints,
		Spec:         w.spec,
	}
	// The workload owns its digest pin; only an image change re-resolves.
	resolvedRepo, resolvedDigest := w.ResolvedRepository, w.ResolvedDigest
	if up.Image != nil && *up.Image != w.Image {
		in.Image = *up.Image
		resolvedRepo, resolvedDigest = nil, nil
	}
	if up.Replicas != nil {
		in.Replicas = *up.Replicas
	}
	if up.DesiredState != nil {
		in.DesiredState = *up.DesiredState
	}
	if up.Constraints != nil {
		in.Constraints = *up.Constraints
	}
	if up.Spec != nil {
		in.Spec = *up.Spec
	}
	if err := validateInput(in); err != nil {
		return store.Workload{}, err
	}

	specJSON, constraintsJSON, err := encodeSpec(in.Spec, in.Constraints)
	if err != nil {
		return store.Workload{}, err
	}
	row, err := s.st.UpdateWorkloadSpec(ctx, store.UpdateWorkloadSpecParams{
		ID:                 id,
		DesiredState:       in.DesiredState,
		Replicas:           int32(in.Replicas),
		Constraints:        constraintsJSON,
		Image:              in.Image,
		Spec:               specJSON,
		ResolvedRepository: resolvedRepo,
		ResolvedDigest:     resolvedDigest,
	})
	if err != nil {
		return store.Workload{}, err
	}
	s.poke()
	return row, nil
}

// Delete marks the workload deleting; the reconciler converges to absence
// and hard-deletes the rows. Idempotent while the teardown runs.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	n, err := s.st.MarkWorkloadDeleting(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrWorkloadNotFound
	}
	s.poke()
	return nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (store.Workload, error) {
	row, err := s.st.GetWorkloadByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Workload{}, ErrWorkloadNotFound
	}
	return row, err
}

func (s *Service) List(ctx context.Context) ([]store.Workload, error) {
	return s.st.ListWorkloads(ctx)
}

// Assignments returns every assignment row with its node name, for the API
// to embed. Grouping by workload is the caller's job.
func (s *Service) Assignments(ctx context.Context) ([]store.ListWorkloadAssignmentsWithNodesRow, error) {
	return s.st.ListWorkloadAssignmentsWithNodes(ctx)
}

func validateInput(in WorkloadInput) error {
	if !workloadNameRe.MatchString(in.Name) {
		return fmt.Errorf("%w: name must match %s", ErrInvalidWorkload, workloadNameRe)
	}
	if in.Kind != engine.KindApplication && in.Kind != engine.KindDatabase {
		// System containers are owned by ensure-loops (registry, later
		// traefik) — two reconcilers must never fight over one container.
		return fmt.Errorf("%w: kind must be application or database", ErrInvalidWorkload)
	}
	if _, err := mirror.ParseImportReference(in.Image); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorkload, err)
	}
	if in.Replicas < 0 {
		return fmt.Errorf("%w: replicas must not be negative", ErrInvalidWorkload)
	}
	if in.DesiredState != DesiredRunning && in.DesiredState != DesiredStopped {
		return fmt.Errorf("%w: desired_state must be running or stopped", ErrInvalidWorkload)
	}
	if err := in.Constraints.validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorkload, err)
	}
	if err := in.Spec.validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorkload, err)
	}
	return nil
}

func encodeSpec(spec Spec, constraints Constraints) (specJSON, constraintsJSON []byte, err error) {
	if specJSON, err = json.Marshal(spec); err != nil {
		return nil, nil, err
	}
	if constraintsJSON, err = json.Marshal(constraints); err != nil {
		return nil, nil, err
	}
	return specJSON, constraintsJSON, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
