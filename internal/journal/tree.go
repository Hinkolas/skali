package journal

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/store"
)

// Tree is one run with its steps nested by parent and their attempts.
type Tree struct {
	Run   store.Run
	Steps []*TreeStep
}

type TreeStep struct {
	Step     store.Step
	Attempts []store.Attempt
	Children []*TreeStep
}

func (s *Service) RunTree(ctx context.Context, runID uuid.UUID) (*Tree, error) {
	run, err := s.st.GetRunByID(ctx, runID)
	if err != nil {
		return nil, notFoundOr(err, "get run")
	}
	steps, err := s.st.ListStepsByRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("journal: list steps: %w", err)
	}
	attempts, err := s.st.ListAttemptsByRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("journal: list attempts: %w", err)
	}

	nodes := make(map[uuid.UUID]*TreeStep, len(steps))
	for _, step := range steps {
		nodes[step.ID] = &TreeStep{Step: step}
	}
	for _, attempt := range attempts {
		if node, ok := nodes[attempt.StepID]; ok {
			node.Attempts = append(node.Attempts, attempt)
		}
	}
	tree := &Tree{Run: run}
	// steps are ordered by created_at, so children follow their parents and
	// the nesting is stable.
	for _, step := range steps {
		node := nodes[step.ID]
		if step.ParentID != nil {
			if parent, ok := nodes[*step.ParentID]; ok {
				parent.Children = append(parent.Children, node)
				continue
			}
		}
		tree.Steps = append(tree.Steps, node)
	}
	return tree, nil
}
