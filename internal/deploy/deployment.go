package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/utils"
)

// ErrDeploymentNotFound: no deployment row with that id.
var ErrDeploymentNotFound = errors.New("deploy: deployment not found")

// DeploymentStatus is the authoritative state of one multi-request
// deployment. The row drives the flow (the journal run only explains it):
// preparing spans the artifact window between opening the deployment and
// completing it, promoted means the revision was created and the target
// moved, and failed or cancelled close the window with values, target, and
// active revision untouched.
type DeploymentStatus string

const (
	DeploymentPreparing DeploymentStatus = "preparing"
	DeploymentPromoted  DeploymentStatus = "promoted"
	DeploymentFailed    DeploymentStatus = "failed"
	DeploymentCancelled DeploymentStatus = "cancelled"
)

// DeploymentStatuses is the deployment lifecycle machine.
var DeploymentStatuses = lifecycle.Machine[DeploymentStatus]{
	States: []DeploymentStatus{
		DeploymentPreparing, DeploymentPromoted,
		DeploymentFailed, DeploymentCancelled,
	},
	Transitions: map[DeploymentStatus][]DeploymentStatus{
		DeploymentPreparing: {DeploymentPromoted, DeploymentFailed, DeploymentCancelled},
	},
}

// ErrInvalidDeploymentTransition: the requested status change is not
// permitted by the deployment lifecycle machine.
var ErrInvalidDeploymentTransition = errors.New("deploy: invalid deployment status transition")

// NewDeployment describes one deployment row to create.
type NewDeployment struct {
	ProjectID           uuid.UUID
	EnvironmentID       uuid.UUID
	DefinitionVersionID uuid.UUID
	CandidateID         uuid.UUID // uuid.Nil deploys current values
	RunID               uuid.UUID
	Actor               string
	BuildExecutor       string
	Actions             json.RawMessage
	// Restart records a forced deployment: promotion stamps a workload
	// restart even when the revision is unchanged.
	Restart bool
	// LocalApplications records the requested intercept set (JSON map of
	// application key to LocalApplication) so completion re-reads it from
	// the row instead of trusting the client a second time. Empty means
	// this deploy clears the environment's intercepts.
	LocalApplications json.RawMessage
	// PruneValues records that promotion unsets the stored values the
	// definition no longer references.
	PruneValues bool
	// BypassProtection records that this deployment went into a
	// promote-only environment on an environment admin's explicit bypass.
	BypassProtection bool
	// FromEnvironmentID records the promotion source; uuid.Nil for a
	// direct deploy.
	FromEnvironmentID uuid.UUID
}

// LastPromotionTargets maps each source environment of the project to the
// environment it was last promoted to, derived from the recorded
// promotions. Any attempt counts: even a failed run states where the team
// routes this source.
func (s *Service) LastPromotionTargets(ctx context.Context, projectID uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	rows, err := s.st.LastPromotionTargets(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("deploy: last promotion targets: %w", err)
	}
	targets := make(map[uuid.UUID]uuid.UUID, len(rows))
	for _, row := range rows {
		if row.FromEnvironmentID != nil {
			targets[*row.FromEnvironmentID] = row.EnvironmentID
		}
	}
	return targets, nil
}

// CreateDeployment inserts the coordination row in preparing. The partial
// unique index turns a concurrent second deployment for the same
// environment into ErrDeploymentInFlight.
func (s *Service) CreateDeployment(ctx context.Context, in NewDeployment) (*store.Deployment, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("deploy: generate id: %w", err)
	}
	executor := in.BuildExecutor
	if executor == "" {
		executor = "local"
	}
	actions := in.Actions
	if len(actions) == 0 {
		actions = json.RawMessage("[]")
	}
	row, err := s.st.CreateDeployment(ctx, store.CreateDeploymentParams{
		ID:                  id,
		ProjectID:           in.ProjectID,
		EnvironmentID:       in.EnvironmentID,
		DefinitionVersionID: in.DefinitionVersionID,
		CandidateID:         utils.NilWhenZero(in.CandidateID),
		RunID:               utils.NilWhenZero(in.RunID),
		Actor:               in.Actor,
		BuildExecutor:       executor,
		Actions:             actions,
		Restart:             in.Restart,
		LocalApplications:   in.LocalApplications,
		PruneValues:         in.PruneValues,
		BypassProtection:    in.BypassProtection,
		FromEnvironmentID:   utils.NilWhenZero(in.FromEnvironmentID),
	})
	if err != nil {
		if store.IsUniqueViolation(err) {
			return nil, ErrDeploymentInFlight
		}
		return nil, fmt.Errorf("deploy: create deployment: %w", err)
	}
	return &row, nil
}

func (s *Service) GetDeployment(ctx context.Context, id uuid.UUID) (*store.Deployment, error) {
	row, err := s.st.GetDeploymentByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDeploymentNotFound
		}
		return nil, fmt.Errorf("deploy: get deployment: %w", err)
	}
	return &row, nil
}

// setDeploymentStatus applies one guarded status change under a row lock,
// optionally recording the revision the deployment produced.
func (s *Service) setDeploymentStatus(ctx context.Context, id uuid.UUID, to DeploymentStatus, revisionID uuid.UUID) error {
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		return setDeploymentStatusTx(ctx, q, id, to, revisionID)
	})
}

// setDeploymentStatusTx is the guarded change itself, so a caller that
// already owns a transaction (Promote) can make the status part of it.
func setDeploymentStatusTx(ctx context.Context, q *store.Queries, id uuid.UUID,
	to DeploymentStatus, revisionID uuid.UUID) error {
	row, err := q.GetDeploymentForUpdate(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeploymentNotFound
		}
		return fmt.Errorf("deploy: lock deployment: %w", err)
	}
	if !DeploymentStatuses.Can(DeploymentStatus(row.Status), to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidDeploymentTransition, row.Status, to)
	}
	if revisionID != uuid.Nil {
		if err := q.SetDeploymentRevision(ctx, store.SetDeploymentRevisionParams{
			ID: id, RevisionID: &revisionID,
		}); err != nil {
			return fmt.Errorf("deploy: set deployment revision: %w", err)
		}
	}
	if err := q.SetDeploymentStatus(ctx, store.SetDeploymentStatusParams{
		ID: id, Status: string(to),
	}); err != nil {
		return fmt.Errorf("deploy: set deployment status: %w", err)
	}
	return nil
}
