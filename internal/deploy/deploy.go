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
	"github.com/Hinkolas/skali/internal/buildstore"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
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
	// ErrAlreadyTargeted: the environment already targets the requested
	// revision.
	ErrAlreadyTargeted = errors.New("deploy: the environment already targets this revision")
)

// ArtifactResolver turns one application source into a verified artifact.
type ArtifactResolver interface {
	Resolve(ctx context.Context, application string, source compiler.ApplicationSource) (artifactstore.Resolved, error)
}

// Enqueuer hands a promoted environment to the reconciliation kernel. It is
// an interface so deploy never imports the kernel; nil means no kernel is
// wired (R1 tests) and Execute finishes its run at promote.
type Enqueuer interface {
	Enqueue(environmentID uuid.UUID)
}

type Service struct {
	st        *store.Store
	values    *valuestore.Service
	artifacts *artifactstore.Service
	builds    *buildstore.Service
	version   string
	enqueuer  Enqueuer
}

func New(st *store.Store, valueSvc *valuestore.Service, artifactSvc *artifactstore.Service, compilerVersion string) *Service {
	return &Service{
		st: st, values: valueSvc, artifacts: artifactSvc,
		builds: buildstore.New(st), version: compilerVersion,
	}
}

// SetEnqueuer wires the reconciliation kernel after construction (the kernel
// depends on this service, so the cycle is broken here).
func (s *Service) SetEnqueuer(e Enqueuer) { s.enqueuer = e }

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
	// Orphaned lists stored value names the definition no longer references;
	// they were ignored, not deployed. Advisory only.
	Orphaned []string
	// Restart makes Promote stamp a workload restart on the target (a
	// forced deployment); Prepare never sets it, the caller does.
	Restart bool
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

	secretVersions, orphaned, err := s.resolveValues(ctx, env.ID, in.CandidateID, definition.RequiredVariables)
	if err != nil {
		return nil, err
	}

	// Artifact resolution runs outside any transaction: real resolvers
	// build and push for minutes. Each resolver call manages its own
	// record lifecycle through the artifact store.
	artifacts := make(map[string]revision.Artifact, len(definition.Applications))
	artifactIDs := make([]uuid.UUID, 0, len(definition.Applications))
	for _, key := range sortedKeys(definition.Applications) {
		source := definition.Applications[key].Source
		if source.Kind == "image" && !source.Image.IsLiteral() {
			reference, err := s.imageReference(ctx, env.ID, in.CandidateID, key, source.Image)
			if err != nil {
				return nil, err
			}
			source.Image = compiler.LiteralExpression(reference)
		}
		resolved, err := in.Resolver.Resolve(ctx, key, source)
		if err != nil {
			return nil, fmt.Errorf("deploy: %w", err)
		}
		artifacts[key] = resolved.Artifact
		artifactIDs = append(artifactIDs, resolved.ArtifactID)
	}

	built, err := revision.Build(revision.Input{
		Result:          &compiler.Result{Hash: definitionVersion.DefinitionHash, Definition: definition},
		Environment:     env.Name,
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
		Orphaned:            orphaned,
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
		if p.Restart {
			if _, err := q.StampEnvironmentRestart(ctx, p.EnvironmentID); err != nil {
				return fmt.Errorf("deploy: stamp restart: %w", err)
			}
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

// RollbackInput describes one rollback: re-point the target at an existing
// revision of the environment under a journaled run of kind "rollback".
type RollbackInput struct {
	EnvironmentID uuid.UUID
	RevisionID    uuid.UUID
	Actor         string
	Journal       *journal.Service
}

type RollbackResult struct {
	RunID uuid.UUID
}

// Rollback points the target at an existing revision of this environment and
// hands rollout to the kernel exactly like a promotion. The run is created
// before the target moves so a failed move leaves a failed run and an
// untouched target; the unique running-run index serializes rollbacks
// against deployments.
func (s *Service) Rollback(ctx context.Context, in RollbackInput) (*RollbackResult, error) {
	row, err := s.st.GetRevisionByID(ctx, in.RevisionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRevisionNotFound
		}
		return nil, fmt.Errorf("deploy: get revision: %w", err)
	}
	if row.EnvironmentID != in.EnvironmentID {
		return nil, ErrRevisionMismatch
	}
	target, err := s.st.GetEnvironmentTarget(ctx, in.EnvironmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("deploy: get target: %w", err)
	}
	// Rolling back to the active revision while the target moved ahead stays
	// allowed: that aborts an in-flight rollout.
	if target.TargetRevisionID != nil && *target.TargetRevisionID == in.RevisionID {
		return nil, ErrAlreadyTargeted
	}
	if _, err := s.st.GetPreparingDeploymentForEnvironment(ctx, in.EnvironmentID); err == nil {
		return nil, ErrDeploymentInFlight
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("deploy: check in-flight deployment: %w", err)
	}

	run, err := in.Journal.CreateRun(ctx, journal.RunInput{
		Kind:          "rollback",
		ProjectID:     row.ProjectID,
		EnvironmentID: in.EnvironmentID,
		Actor:         in.Actor,
	})
	if err != nil {
		return nil, err
	}
	if err := in.Journal.StartRun(ctx, run.ID); err != nil {
		if errors.Is(err, journal.ErrRunConflict) {
			return nil, ErrDeploymentInFlight
		}
		return nil, err
	}

	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		rows, err := q.SetEnvironmentTarget(ctx, store.SetEnvironmentTargetParams{
			EnvironmentID:    in.EnvironmentID,
			TargetRevisionID: &in.RevisionID,
		})
		if err != nil {
			return fmt.Errorf("deploy: set target: %w", err)
		}
		if rows == 0 {
			return ErrEnvironmentNotFound
		}
		return nil
	})
	if err != nil {
		return nil, finishRunFailed(ctx, in.Journal, run.ID, err)
	}

	redactor, err := s.values.Redactor(ctx, in.EnvironmentID, uuid.Nil)
	if err != nil {
		return nil, finishRunFailed(ctx, in.Journal, run.ID, err)
	}
	if err := s.instantStep(ctx, in.Journal, run.ID, redactor, "promote", "Promote revision",
		"target set to revision "+row.Checksum); err != nil {
		return nil, finishRunFailed(ctx, in.Journal, run.ID, err)
	}
	if s.enqueuer != nil {
		if _, err := in.Journal.EnsureStep(ctx, run.ID, nil, "rollout", "Roll out revision"); err != nil {
			return nil, finishRunFailed(ctx, in.Journal, run.ID, err)
		}
		s.enqueuer.Enqueue(in.EnvironmentID)
		return &RollbackResult{RunID: run.ID}, nil
	}
	if err := in.Journal.FinishRun(ctx, run.ID, journal.RunSucceeded); err != nil {
		return nil, err
	}
	return &RollbackResult{RunID: run.ID}, nil
}

// finishRunFailed concludes a run after a failure and returns the original
// error.
func finishRunFailed(ctx context.Context, jsvc *journal.Service, runID uuid.UUID, cause error) error {
	if err := jsvc.FinishRun(ctx, runID, journal.RunFailed); err != nil &&
		!errors.Is(err, journal.ErrInvalidTransition) {
		return fmt.Errorf("deploy: finish run after failure: %w (original: %w)", err, cause)
	}
	return cause
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

// storedVersions merges the environment's current value versions with the
// staged candidate batch (staged wins per name). Plaintexts are never
// loaded here.
func (s *Service) storedVersions(ctx context.Context, environmentID, candidateID uuid.UUID) (map[string]int, error) {
	versions, err := s.values.CurrentVersions(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	if candidateID != uuid.Nil {
		staged, err := s.values.StagedVersions(ctx, environmentID, candidateID)
		if err != nil {
			return nil, err
		}
		maps.Copy(versions, staged)
	}
	provided := make(map[string]int, len(versions))
	for name, version := range versions {
		provided[name] = int(version)
	}
	return provided, nil
}

// resolveValues intersects the environment's stored value versions with the
// definition's runtime requirements. Orphaned names (stored but no longer
// referenced by the definition) are reported for advisory surfacing, never
// as errors: a removed reference must not wedge the environment.
func (s *Service) resolveValues(ctx context.Context, environmentID, candidateID uuid.UUID,
	requirements []compiler.VariableRequirement) (map[string]int, []string, error) {

	provided, err := s.storedVersions(ctx, environmentID, candidateID)
	if err != nil {
		return nil, nil, err
	}
	kept, _, orphaned := values.Conform(requirements, provided)
	return kept, orphaned, nil
}

// imageReference resolves an application's image reference. Literal
// references (the normal case) resolve without touching the store; an
// expression decrypts exactly the referenced values, pinned at the versions
// a deployment would use. A missing value surfaces as a ValuesError naming
// the variable, never a plaintext.
func (s *Service) imageReference(ctx context.Context, environmentID, candidateID uuid.UUID,
	application string, image compiler.Expression) (string, error) {

	if image.IsLiteral() {
		return image.Literal(), nil
	}
	versions, err := s.storedVersions(ctx, environmentID, candidateID)
	if err != nil {
		return "", err
	}
	needed := make(map[string]int)
	for _, part := range image.Parts {
		if part.Kind != "project_variable" {
			continue
		}
		if version, ok := versions[part.Name]; ok {
			needed[part.Name] = version
		}
	}
	plaintexts, err := s.values.Plaintexts(ctx, environmentID, needed)
	if err != nil {
		return "", err
	}
	resolved, err := compiler.ResolveExpression(image, plaintexts)
	if err != nil {
		return "", &revision.ValuesError{Message: fmt.Sprintf("application %s image: %s", application, err)}
	}
	return resolved, nil
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
