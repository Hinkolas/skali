package journal

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/store"
)

// RecoverOnBoot fails every running attempt whose executor no longer exists
// (its executor_id differs from this boot's) and appends one restart
// diagnostic as the attempt's final log entry. It touches nothing else:
// runs keep their status, steps keep theirs, and targets are never read or
// written. The controller reattaches through the deterministic step keys
// and continues toward the unchanged target.
func (s *Service) RecoverOnBoot(ctx context.Context) (int64, error) {
	orphans, err := s.st.ListForeignRunningAttempts(ctx, s.executorID)
	if err != nil {
		return 0, fmt.Errorf("journal: list orphaned attempts: %w", err)
	}
	var failed int64
	for _, orphan := range orphans {
		err := s.st.WithTx(ctx, func(q *store.Queries) error {
			attempt, err := q.GetAttemptForUpdate(ctx, orphan.ID)
			if err != nil {
				return notFoundOr(err, "lock attempt")
			}
			// Re-check under the lock; another instance may have closed it.
			if attempt.Status != string(AttemptRunning) || attempt.ExecutorID == s.executorID {
				return nil
			}
			maxSeq, err := q.GetMaxRunLogSeq(ctx, attempt.ID)
			if err != nil {
				return fmt.Errorf("journal: max seq: %w", err)
			}
			if maxSeq < MaxEntriesPerAttempt {
				id, err := uuid.NewV7()
				if err != nil {
					return fmt.Errorf("journal: generate id: %w", err)
				}
				if _, err := q.AppendRunLog(ctx, store.AppendRunLogParams{
					ID:        id,
					AttemptID: attempt.ID,
					Seq:       maxSeq + 1,
					Level:     "error",
					Message:   "attempt failed: daemon restarted while it was running",
					Fields:    []byte("{}"),
				}); err != nil {
					return fmt.Errorf("journal: append restart diagnostic: %w", err)
				}
			}
			if err := q.MarkAttemptFinished(ctx, store.MarkAttemptFinishedParams{
				ID: attempt.ID, Status: string(AttemptFailed),
			}); err != nil {
				return fmt.Errorf("journal: fail attempt: %w", err)
			}
			failed++
			return nil
		})
		if err != nil {
			return failed, err
		}
	}
	return failed, nil
}
