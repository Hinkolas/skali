package project

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/compiler"
)

// EnvironmentSummary is one environment's row in a project summary: the
// pointer state comes straight from environment_targets; health is filled in
// by the caller (the API layer holds the reconcile kernel).
type EnvironmentSummary struct {
	ID    uuid.UUID
	Name  string
	State string
}

// ServiceCounts are the per-type service counts of a project's draft
// definition; a project without a draft has zeros.
type ServiceCounts struct {
	Applications int
	Databases    int
	Buckets      int
}

// Summary is the project-list rollup: environments with their states plus
// draft service counts.
type Summary struct {
	Environments  []EnvironmentSummary
	ServiceCounts ServiceCounts
}

// ListSummaries builds the rollup for every project in two queries: all
// environments with target states, and all draft definitions. Projects
// without environments or drafts still get an entry with empty fields.
func (s *Service) ListSummaries(ctx context.Context) (map[uuid.UUID]Summary, error) {
	summaries := make(map[uuid.UUID]Summary)

	environments, err := s.st.ListEnvironmentsWithTargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("project: list environment targets: %w", err)
	}
	for _, env := range environments {
		summary := summaries[env.ProjectID]
		summary.Environments = append(summary.Environments, EnvironmentSummary{
			ID:    env.ID,
			Name:  env.Name,
			State: env.State,
		})
		summaries[env.ProjectID] = summary
	}

	drafts, err := s.st.ListDraftDefinitions(ctx)
	if err != nil {
		return nil, fmt.Errorf("project: list draft definitions: %w", err)
	}
	for _, draft := range drafts {
		var definition compiler.ProjectDefinition
		if err := json.Unmarshal(draft.Definition, &definition); err != nil {
			return nil, fmt.Errorf("project: decode draft definition: %w", err)
		}
		summary := summaries[draft.ProjectID]
		summary.ServiceCounts = ServiceCounts{
			Applications: len(definition.Applications),
			Databases:    len(definition.Databases),
			Buckets:      len(definition.Buckets),
		}
		summaries[draft.ProjectID] = summary
	}

	return summaries, nil
}
