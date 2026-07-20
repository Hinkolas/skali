// Package deploy owns candidate preparation and promotion: it turns a
// stored definition version plus environment values plus resolved artifacts
// into an immutable revision, and moves the environment target atomically
// with value and draft promotion. Artifact resolution is a pluggable seam;
// R1 ships only the fake resolver, the real build and import resolvers are
// R3 and R4.
package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/values"
	"github.com/Hinkolas/skali/internal/valuestore"
)

var (
	ErrEnvironmentNotFound = errors.New("deploy: environment not found")
	ErrRevisionNotFound    = errors.New("deploy: revision not found")
	// ErrDefinitionMismatch: the definition version belongs to another
	// project than the environment.
	ErrDefinitionMismatch = errors.New("deploy: definition version does not belong to the project")
	// ErrRevisionMismatch: the revision belongs to another environment.
	ErrRevisionMismatch = errors.New("deploy: revision does not belong to the environment")
)

// ArtifactResolver turns one application source into a verified artifact.
type ArtifactResolver interface {
	Resolve(ctx context.Context, application string, source compiler.ApplicationSource) (artifactstore.Resolved, error)
}

type Service struct {
	st        *store.Store
	values    *valuestore.Service
	artifacts *artifactstore.Service
	version   string
}

func New(st *store.Store, valueSvc *valuestore.Service, artifactSvc *artifactstore.Service, compilerVersion string) *Service {
	return &Service{st: st, values: valueSvc, artifacts: artifactSvc, version: compilerVersion}
}

type PrepareInput struct {
	EnvironmentID       uuid.UUID
	DefinitionVersionID uuid.UUID
	// CandidateID selects a staged values batch; uuid.Nil deploys the
	// environment's current values unchanged.
	CandidateID uuid.UUID
	Resolver    ArtifactResolver
}

// Prepared carries everything Promote needs; it exists only in memory.
type Prepared struct {
	RevisionID          uuid.UUID
	Revision            *revision.Revision
	ProjectID           uuid.UUID
	EnvironmentID       uuid.UUID
	DefinitionVersionID uuid.UUID
	CandidateID         uuid.UUID
	ArtifactIDs         []uuid.UUID
}

// Prepare loads and re-validates the inputs, resolves artifacts outside any
// transaction, builds the pure revision, and stores it with its leases. Any
// failure leaves the target untouched and staged values merely staged.
func (s *Service) Prepare(ctx context.Context, in PrepareInput) (*Prepared, error) {
	env, err := s.st.GetEnvironmentByID(ctx, in.EnvironmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("deploy: get environment: %w", err)
	}
	definitionVersion, err := s.st.GetDefinitionVersionByID(ctx, in.DefinitionVersionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("deploy: definition version not found")
		}
		return nil, fmt.Errorf("deploy: get definition version: %w", err)
	}
	if definitionVersion.ProjectID != env.ProjectID {
		return nil, ErrDefinitionMismatch
	}
	var definition compiler.ProjectDefinition
	if err := json.Unmarshal(definitionVersion.Definition, &definition); err != nil {
		return nil, fmt.Errorf("deploy: decode definition: %w", err)
	}

	resolvedValues, secretVersions, err := s.resolveValues(ctx, env.ID, in.CandidateID)
	if err != nil {
		return nil, err
	}

	// Artifact resolution runs outside any transaction: real resolvers
	// build and push for minutes. Each resolver call manages its own
	// record lifecycle through the artifact store.
	artifacts := make(map[string]revision.Artifact, len(definition.Applications))
	artifactIDs := make([]uuid.UUID, 0, len(definition.Applications))
	for _, key := range sortedKeys(definition.Applications) {
		resolved, err := in.Resolver.Resolve(ctx, key, definition.Applications[key].Source)
		if err != nil {
			return nil, fmt.Errorf("deploy: %w", err)
		}
		artifacts[key] = resolved.Artifact
		artifactIDs = append(artifactIDs, resolved.ArtifactID)
	}

	built, err := revision.Build(revision.Input{
		Result:          &compiler.Result{Hash: definitionVersion.DefinitionHash, Definition: definition},
		Environment:     env.Name,
		Values:          resolvedValues,
		SecretVersions:  secretVersions,
		Artifacts:       artifacts,
		CompilerVersion: s.version,
	})
	if err != nil {
		return nil, fmt.Errorf("deploy: build revision: %w", err)
	}
	document, err := json.Marshal(built)
	if err != nil {
		return nil, fmt.Errorf("deploy: encode revision: %w", err)
	}

	prepared := &Prepared{
		Revision:            built,
		ProjectID:           env.ProjectID,
		EnvironmentID:       env.ID,
		DefinitionVersionID: definitionVersion.ID,
		CandidateID:         in.CandidateID,
		ArtifactIDs:         artifactIDs,
	}
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("deploy: generate id: %w", err)
		}
		if _, err := q.InsertRevision(ctx, store.InsertRevisionParams{
			ID:                  id,
			ProjectID:           env.ProjectID,
			EnvironmentID:       env.ID,
			DefinitionVersionID: definitionVersion.ID,
			SchemaVersion:       built.SchemaVersion,
			Checksum:            built.Checksum,
			DefinitionHash:      built.DefinitionHash,
			ValuesHash:          built.ValuesHash,
			CompilerVersion:     built.CompilerVersion,
			Document:            document,
		}); err != nil {
			return fmt.Errorf("deploy: insert revision: %w", err)
		}
		// Identical inputs reuse the stored row; read back the canonical id.
		row, err := q.GetRevisionByChecksum(ctx, store.GetRevisionByChecksumParams{
			EnvironmentID: env.ID,
			Checksum:      built.Checksum,
		})
		if err != nil {
			return fmt.Errorf("deploy: read revision: %w", err)
		}
		prepared.RevisionID = row.ID
		return s.artifacts.LeaseTx(ctx, q, row.ID, artifactIDs)
	})
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

// Promote is the single atomic transaction of a deployment: move the target
// pointer, promote the candidate's staged values and secrets, and advance
// the project draft to the promoted definition when it differs. All or
// nothing; this is the only writer of target_revision_id besides Rollback.
func (s *Service) Promote(ctx context.Context, p *Prepared) error {
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		rows, err := q.SetEnvironmentTarget(ctx, store.SetEnvironmentTargetParams{
			EnvironmentID:    p.EnvironmentID,
			TargetRevisionID: &p.RevisionID,
		})
		if err != nil {
			return fmt.Errorf("deploy: set target: %w", err)
		}
		if rows == 0 {
			return ErrEnvironmentNotFound
		}
		if err := s.values.PromoteTx(ctx, q, p.EnvironmentID, p.CandidateID); err != nil {
			return err
		}
		if _, err := q.AdvanceProjectDraft(ctx, store.AdvanceProjectDraftParams{
			ProjectID:           p.ProjectID,
			DefinitionVersionID: p.DefinitionVersionID,
		}); err != nil {
			return fmt.Errorf("deploy: advance draft: %w", err)
		}
		return nil
	})
}

// Rollback points the target at an existing revision of this environment.
func (s *Service) Rollback(ctx context.Context, environmentID, revisionID uuid.UUID) error {
	row, err := s.st.GetRevisionByID(ctx, revisionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRevisionNotFound
		}
		return fmt.Errorf("deploy: get revision: %w", err)
	}
	if row.EnvironmentID != environmentID {
		return ErrRevisionMismatch
	}
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		rows, err := q.SetEnvironmentTarget(ctx, store.SetEnvironmentTargetParams{
			EnvironmentID:    environmentID,
			TargetRevisionID: &revisionID,
		})
		if err != nil {
			return fmt.Errorf("deploy: set target: %w", err)
		}
		if rows == 0 {
			return ErrEnvironmentNotFound
		}
		return nil
	})
}

func (s *Service) Target(ctx context.Context, environmentID uuid.UUID) (*store.EnvironmentTarget, error) {
	row, err := s.st.GetEnvironmentTarget(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("deploy: get target: %w", err)
	}
	return &row, nil
}

func (s *Service) ListRevisions(ctx context.Context, environmentID uuid.UUID) ([]store.ListRevisionsRow, error) {
	rows, err := s.st.ListRevisions(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("deploy: list revisions: %w", err)
	}
	return rows, nil
}

// GetRevision decodes the stored canonical document.
func (s *Service) GetRevision(ctx context.Context, id uuid.UUID) (*revision.Revision, error) {
	row, err := s.st.GetRevisionByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRevisionNotFound
		}
		return nil, fmt.Errorf("deploy: get revision: %w", err)
	}
	var document revision.Revision
	if err := json.Unmarshal(row.Document, &document); err != nil {
		return nil, fmt.Errorf("deploy: decode revision: %w", err)
	}
	return &document, nil
}

// resolveValues merges the environment's current values with the staged
// candidate batch (staged wins per name). Secret plaintexts are never
// loaded: the resolved secret set carries names only, and versions come
// from the store.
func (s *Service) resolveValues(ctx context.Context, environmentID, candidateID uuid.UUID) (values.Resolved, map[string]int, error) {
	plain, err := s.values.CurrentPlain(ctx, environmentID)
	if err != nil {
		return values.Resolved{}, nil, err
	}
	versions, err := s.values.CurrentSecretVersions(ctx, environmentID)
	if err != nil {
		return values.Resolved{}, nil, err
	}
	if candidateID != uuid.Nil {
		stagedPlain, err := s.values.StagedPlain(ctx, environmentID, candidateID)
		if err != nil {
			return values.Resolved{}, nil, err
		}
		maps.Copy(plain, stagedPlain)
		stagedVersions, err := s.values.StagedSecretVersions(ctx, environmentID, candidateID)
		if err != nil {
			return values.Resolved{}, nil, err
		}
		maps.Copy(versions, stagedVersions)
	}
	resolved := values.Resolved{
		Plain:  plain,
		Secret: make(map[string]string, len(versions)),
	}
	secretVersions := make(map[string]int, len(versions))
	for name, version := range versions {
		resolved.Secret[name] = ""
		secretVersions[name] = int(version)
	}
	return resolved, secretVersions, nil
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
