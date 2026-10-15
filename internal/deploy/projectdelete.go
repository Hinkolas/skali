package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
)

var (
	ErrProjectNotFound = errors.New("deploy: project not found")
	// ErrProjectDeleting: the project is being deleted, so it accepts no
	// new deployments.
	ErrProjectDeleting = errors.New("deploy: the project is being deleted")
)

// ProjectDeletion is a project delete's outcome. A project without
// environments is gone at once (Deleted); otherwise every environment is
// purging and the last purge deletes the project row.
type ProjectDeletion struct {
	Deleted bool
	Purges  []EnvironmentPurge
}

// EnvironmentPurge names one environment's purge. RunID is the run this
// delete started; an environment that was already releasing keeps the
// purge it has, and RunID stays nil.
type EnvironmentPurge struct {
	EnvironmentID uuid.UUID
	RunID         *uuid.UUID
}

// DeleteProject deletes a project with everything it runs. The decision is
// persisted on the project row first, so it survives a failed request and
// the daemon; then every environment is purged exactly as skali env remove
// would. Repeating the call resumes a delete an error interrupted: it marks
// nothing new and only purges the environments not yet releasing.
func (s *Service) DeleteProject(ctx context.Context, projectID uuid.UUID, jr *journal.Service, actor string) (*ProjectDeletion, error) {
	// A client still building against an environment would make its purge
	// refuse halfway through; refuse before marking anything instead.
	states, err := s.st.ListProjectEnvironmentStates(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("deploy: list environment states: %w", err)
	}
	for _, env := range states {
		if _, err := s.st.GetPreparingDeploymentForEnvironment(ctx, env.ID); err == nil {
			return nil, ErrDeploymentInFlight
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("deploy: check in-flight deployment: %w", err)
		}
	}

	deleted := false
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		// Environment creation takes the same lock and refuses a marked
		// project, so the list read after the mark is complete.
		if _, err := q.GetProjectForUpdate(ctx, projectID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProjectNotFound
			}
			return fmt.Errorf("deploy: lock project: %w", err)
		}
		if _, err := q.MarkProjectDeleting(ctx, projectID); err != nil {
			return fmt.Errorf("deploy: mark project deleting: %w", err)
		}
		rows, err := q.DeleteDeletingProjectIfEmpty(ctx, projectID)
		if err != nil {
			return fmt.Errorf("deploy: delete empty project: %w", err)
		}
		deleted = rows > 0
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.feed.Publish(projectID)
	if deleted {
		return &ProjectDeletion{Deleted: true}, nil
	}

	states, err = s.st.ListProjectEnvironmentStates(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("deploy: list environment states: %w", err)
	}
	deletion := &ProjectDeletion{Purges: make([]EnvironmentPurge, 0, len(states))}
	for _, env := range states {
		if env.State == EnvironmentStateReleasing {
			deletion.Purges = append(deletion.Purges, EnvironmentPurge{EnvironmentID: env.ID})
			continue
		}
		run, _, err := s.Teardown(ctx, env.ID, true, jr, actor)
		if errors.Is(err, ErrEnvironmentNotFound) {
			continue // purged and deleted in the meantime
		}
		if err != nil {
			return nil, err
		}
		deletion.Purges = append(deletion.Purges, EnvironmentPurge{EnvironmentID: env.ID, RunID: &run.ID})
	}
	return deletion, nil
}

// checkOpenable refuses a deployment into an environment that is being
// purged or whose project is being deleted: either would only end in a
// refused promote, after the build.
func (s *Service) checkOpenable(ctx context.Context, env store.Environment) error {
	proj, err := s.st.GetProjectByID(ctx, env.ProjectID)
	if err != nil {
		return fmt.Errorf("deploy: get project: %w", err)
	}
	if proj.DeletingAt != nil {
		return ErrProjectDeleting
	}
	target, err := s.st.GetEnvironmentTarget(ctx, env.ID)
	if err != nil {
		return fmt.Errorf("deploy: get environment target: %w", err)
	}
	if target.State == EnvironmentStateReleasing {
		return ErrEnvironmentReleasing
	}
	return nil
}
