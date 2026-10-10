package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FinishRun finishes a run (see the FinishRun query) once every journal
// write in flight under it has committed. A write that adds a step or an
// attempt holds the run row KEY SHARE, which a lone update does not wait
// for, and one statement cannot see rows committed after its snapshot. So
// LockRunForFinish goes first, and both statements go out as one batch:
// one round trip and one implicit transaction, in which the second
// statement's snapshot is taken after the lock waited those writes out.
func (s *Store) FinishRun(ctx context.Context, arg FinishRunParams) (*uuid.UUID, error) {
	var environmentID *uuid.UUID
	batch := &pgx.Batch{}
	batch.Queue(lockRunForFinish, arg.ID)
	batch.Queue(finishRun, arg.Status, arg.Failure, arg.ID, arg.FromStatuses, arg.CloseStatus).
		QueryRow(func(row pgx.Row) error { return row.Scan(&environmentID) })
	if err := s.Pool.SendBatch(ctx, batch).Close(); err != nil {
		return nil, err
	}
	return environmentID, nil
}
