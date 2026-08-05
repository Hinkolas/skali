package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/store"
	versionpkg "github.com/Hinkolas/skali/internal/version"
	"github.com/Hinkolas/skali/internal/yamldoc"
)

// Draft is the current editable definition document of a project together
// with its compiled form. Version is the optimistic concurrency token.
type Draft struct {
	Version    int64
	Format     string
	Source     []byte
	Hash       string
	Definition compiler.ProjectDefinition
}

// DraftSubmission replaces the whole draft document. ExpectedVersion must
// match the stored draft version; 0 is accepted only for the first submission.
type DraftSubmission struct {
	Source          []byte
	Format          string
	ExpectedVersion int64
}

// SubmitDraft parses, validates, and compiles the manifest entirely in
// memory; any diagnostic fails before a single row is written. On success it
// upserts the content-addressed definition version and advances the draft
// with a compare-and-swap on the expected version.
func (s *Service) SubmitDraft(ctx context.Context, projectID uuid.UUID, in DraftSubmission) (*Draft, error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	result, err := compileSource(in.Source, in.Format)
	if err != nil {
		return nil, err
	}
	if result.Definition.Name != proj.Name {
		return nil, ErrNameMismatch
	}

	var draft *Draft
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		version, err := upsertDefinitionVersion(ctx, q, projectID, result, in.Source, in.Format)
		if err != nil {
			return err
		}
		current, err := q.GetProjectDraft(ctx, projectID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if in.ExpectedVersion != 0 {
				return ErrVersionConflict
			}
			current, err = q.CreateProjectDraft(ctx, store.CreateProjectDraftParams{
				ProjectID:           projectID,
				DefinitionVersionID: version.ID,
			})
			if err != nil {
				return fmt.Errorf("project: create draft: %w", err)
			}
		case err != nil:
			return fmt.Errorf("project: get draft: %w", err)
		default:
			if in.ExpectedVersion == 0 {
				return ErrVersionConflict
			}
			rows, err := q.UpdateProjectDraft(ctx, store.UpdateProjectDraftParams{
				ProjectID:           projectID,
				Version:             in.ExpectedVersion,
				DefinitionVersionID: version.ID,
			})
			if err != nil {
				return fmt.Errorf("project: update draft: %w", err)
			}
			if rows == 0 {
				return ErrVersionConflict
			}
			current, err = q.GetProjectDraft(ctx, projectID)
			if err != nil {
				return fmt.Errorf("project: reread draft: %w", err)
			}
		}
		draft = &Draft{
			Version:    current.Version,
			Format:     in.Format,
			Source:     in.Source,
			Hash:       result.Hash,
			Definition: result.Definition,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return draft, nil
}

func (s *Service) GetDraft(ctx context.Context, projectID uuid.UUID) (*Draft, error) {
	if _, err := s.Get(ctx, projectID); err != nil {
		return nil, err
	}
	row, err := s.st.GetProjectDraft(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDraftNotFound
		}
		return nil, fmt.Errorf("project: get draft: %w", err)
	}
	version, err := s.st.GetDefinitionVersionByID(ctx, row.DefinitionVersionID)
	if err != nil {
		return nil, fmt.Errorf("project: get definition version: %w", err)
	}
	definition, err := compiler.DecodeDefinition(version.Definition)
	if err != nil {
		return nil, err
	}
	return &Draft{
		Version:    row.Version,
		Format:     version.Format,
		Source:     version.Source,
		Hash:       version.DefinitionHash,
		Definition: definition,
	}, nil
}

// SubmitCandidate compiles and stores a definition version WITHOUT moving the
// draft. Deploy preparation uses it for candidate documents; only promotion
// advances the draft, atomically with the target.
func (s *Service) SubmitCandidate(ctx context.Context, projectID uuid.UUID, source []byte, format string) (uuid.UUID, string, error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return uuid.Nil, "", err
	}
	result, err := compileSource(source, format)
	if err != nil {
		return uuid.Nil, "", err
	}
	if result.Definition.Name != proj.Name {
		return uuid.Nil, "", ErrNameMismatch
	}
	var id uuid.UUID
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		version, err := upsertDefinitionVersion(ctx, q, projectID, result, source, format)
		if err != nil {
			return err
		}
		id = version.ID
		return nil
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	return id, result.Hash, nil
}

// compileSource runs the full in-memory pipeline: strict parse, validation,
// compilation. The returned error is yamldoc.Diagnostics when the document
// itself is at fault, so callers can render positions.
func compileSource(source []byte, format string) (*compiler.Result, error) {
	if format != "yaml" && format != "json" {
		return nil, ErrInvalidFormat
	}
	document, err := manifest.Parse(source, "skali."+shortFormat(format))
	if err != nil {
		var diagnostic yamldoc.Diagnostic
		if errors.As(err, &diagnostic) {
			return nil, yamldoc.Diagnostics{diagnostic}
		}
		return nil, err
	}
	return compiler.Compile(document)
}

func shortFormat(format string) string {
	if format == "yaml" {
		return "yml"
	}
	return format
}

// upsertDefinitionVersion inserts the content-addressed compiled definition
// and returns the canonical row, which may predate this call.
func upsertDefinitionVersion(ctx context.Context, q *store.Queries, projectID uuid.UUID, result *compiler.Result, source []byte, format string) (*store.DefinitionVersion, error) {
	canonical, err := json.Marshal(result.Definition)
	if err != nil {
		return nil, fmt.Errorf("project: encode definition: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("project: generate id: %w", err)
	}
	if _, err := q.InsertDefinitionVersion(ctx, store.InsertDefinitionVersionParams{
		ID:              id,
		ProjectID:       projectID,
		SchemaVersion:   result.Definition.Version,
		DefinitionHash:  result.Hash,
		Definition:      canonical,
		Source:          source,
		Format:          format,
		CompilerVersion: versionpkg.Version,
	}); err != nil {
		return nil, fmt.Errorf("project: insert definition version: %w", err)
	}
	row, err := q.GetDefinitionVersionByHash(ctx, store.GetDefinitionVersionByHashParams{
		ProjectID:      projectID,
		DefinitionHash: result.Hash,
	})
	if err != nil {
		return nil, fmt.Errorf("project: read definition version: %w", err)
	}
	return &row, nil
}
