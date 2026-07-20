// Package artifactstore persists managed-registry artifact records and
// guards every phase change with the internal/artifact lifecycle machine.
// R1 stores records and leases only; the pipelines that push real content
// into the registry are R3 (local builds, imports) and R4 (production).
package artifactstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/artifact"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
)

// SafetyWindow is the R1 constant hold on release artifacts: even without a
// lease, a build artifact cannot be evicted until this long after it was
// verified. Cache imports are reconstructable and carry no window.
const SafetyWindow = 72 * time.Hour

var (
	ErrNotFound = errors.New("artifactstore: not found")
	// ErrInvalidTransition: the requested phase change is not permitted by
	// the artifact lifecycle machine.
	ErrInvalidTransition = errors.New("artifactstore: invalid phase transition")
	// ErrLeased: at least one retained revision leases this artifact.
	ErrLeased = errors.New("artifactstore: artifact is leased by a revision")
	// ErrSafetyWindow: a release artifact is still inside its post-verify
	// safety window.
	ErrSafetyWindow = errors.New("artifactstore: artifact is inside its safety window")
)

type Service struct {
	st *store.Store
}

func New(st *store.Store) *Service {
	return &Service{st: st}
}

// Pending describes the expected content of one build or import.
type Pending struct {
	ProjectID   uuid.UUID
	Application string
	Kind        string // revision.KindImport | KindBuildLocal | KindBuildCloud
	Upstream    string
	ContextHash string
}

func (s *Service) CreatePending(ctx context.Context, in Pending) (*store.Artifact, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("artifactstore: generate id: %w", err)
	}
	var projectID *uuid.UUID
	if in.ProjectID != uuid.Nil {
		projectID = &in.ProjectID
	}
	row, err := s.st.CreateArtifact(ctx, store.CreateArtifactParams{
		ID:          id,
		ProjectID:   projectID,
		Application: in.Application,
		Kind:        in.Kind,
		Upstream:    in.Upstream,
		ContextHash: in.ContextHash,
	})
	if err != nil {
		return nil, fmt.Errorf("artifactstore: create pending: %w", err)
	}
	return &row, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*store.Artifact, error) {
	row, err := s.st.GetArtifactByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("artifactstore: get: %w", err)
	}
	return &row, nil
}

// Verify records the confirmed registry content and moves the record to
// verified, guarded by the lifecycle machine under a row lock.
func (s *Service) Verify(ctx context.Context, id uuid.UUID, reference, digest string, provenance json.RawMessage) error {
	if len(provenance) == 0 {
		provenance = json.RawMessage("{}")
	}
	return s.transition(ctx, id, artifact.PhaseVerified, func(q *store.Queries) error {
		return q.SetArtifactVerified(ctx, store.SetArtifactVerifiedParams{
			ID:         id,
			Reference:  reference,
			Digest:     &digest,
			Provenance: provenance,
		})
	})
}

// Abandon closes a record whose build or import ended without verified
// content; the run carries the failure detail.
func (s *Service) Abandon(ctx context.Context, id uuid.UUID) error {
	return s.transition(ctx, id, artifact.PhaseAbandoned, func(q *store.Queries) error {
		return q.SetArtifactPhase(ctx, store.SetArtifactPhaseParams{
			ID: id, Phase: string(artifact.PhaseAbandoned),
		})
	})
}

// Evict reclaims verified content. It refuses while any revision leases the
// artifact, and release artifacts additionally hold the safety window.
func (s *Service) Evict(ctx context.Context, id uuid.UUID) error {
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		row, err := q.GetArtifactForUpdate(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("artifactstore: lock: %w", err)
		}
		if !artifact.Phases.Can(artifact.Phase(row.Phase), artifact.PhaseEvicted) {
			return fmt.Errorf("%w: %s -> evicted", ErrInvalidTransition, row.Phase)
		}
		leases, err := q.CountArtifactLeases(ctx, id)
		if err != nil {
			return fmt.Errorf("artifactstore: count leases: %w", err)
		}
		if leases > 0 {
			return ErrLeased
		}
		if row.Kind != revision.KindImport && row.VerifiedAt != nil &&
			time.Since(*row.VerifiedAt) < SafetyWindow {
			return ErrSafetyWindow
		}
		return q.SetArtifactPhase(ctx, store.SetArtifactPhaseParams{
			ID: id, Phase: string(artifact.PhaseEvicted),
		})
	})
}

// LeaseTx records the revision's leases inside the caller's transaction.
func (s *Service) LeaseTx(ctx context.Context, q *store.Queries, revisionID uuid.UUID, artifactIDs []uuid.UUID) error {
	for _, artifactID := range artifactIDs {
		if err := q.CreateArtifactLease(ctx, store.CreateArtifactLeaseParams{
			RevisionID: revisionID,
			ArtifactID: artifactID,
		}); err != nil {
			return fmt.Errorf("artifactstore: lease %s: %w", artifactID, err)
		}
	}
	return nil
}

// SweepPending abandons pending records older than age; the safety net for
// executors that died without closing their record.
func (s *Service) SweepPending(ctx context.Context, age time.Duration) (int64, error) {
	rows, err := s.st.SweepPendingArtifacts(ctx, time.Now().Add(-age))
	if err != nil {
		return 0, fmt.Errorf("artifactstore: sweep pending: %w", err)
	}
	return rows, nil
}

// transition applies one guarded phase change under a row lock.
func (s *Service) transition(ctx context.Context, id uuid.UUID, to artifact.Phase, apply func(q *store.Queries) error) error {
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		row, err := q.GetArtifactForUpdate(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("artifactstore: lock: %w", err)
		}
		if !artifact.Phases.Can(artifact.Phase(row.Phase), to) {
			return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, row.Phase, to)
		}
		return apply(q)
	})
}
