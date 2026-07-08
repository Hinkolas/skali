// Package operations records task-shaped background work kicked off by a
// user request: start it, detach it from the request, and let clients poll
// the row. It is deliberately thin — state-shaped work (workload
// convergence) lives on desired-state rows with phases, not here. Liveness
// needs no owner bookkeeping: every operation carries a deadline, and a row
// still running past it belongs to a dead or wedged master, failed by the
// hygiene sweep (FailExpiredOperations).
package operations

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/store"
)

// KindRegistryImport is a mirror import: subject is the upstream reference,
// result carries the catalog row on success.
const KindRegistryImport = "registry_import"

// Statuses mirror the table's CHECK constraint.
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// expirySlack pads the row deadline past the work's own timeout so an
// in-process completion always wins the race against the hygiene sweep.
const expirySlack = time.Minute

// Start records the operation and runs fn detached from the caller's request
// (the work finishes server-side even when the client gives up waiting),
// bounded by timeout. The returned row is what the caller hands back with
// its 202; the outcome lands on the same row for polling.
func Start(ctx context.Context, st *store.Store, kind, subject string, createdBy uuid.UUID, timeout time.Duration, fn func(context.Context) (json.RawMessage, error)) (store.Operation, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return store.Operation{}, err
	}
	op, err := st.InsertOperation(ctx, store.InsertOperationParams{
		ID:        id,
		Kind:      kind,
		Subject:   subject,
		CreatedBy: &createdBy,
		ExpiresAt: time.Now().Add(timeout + expirySlack),
	})
	if err != nil {
		return store.Operation{}, err
	}

	go func() {
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		result, err := fn(runCtx)

		status, errText := StatusSucceeded, (*string)(nil)
		if err != nil {
			msg := err.Error()
			status, errText = StatusFailed, &msg
		}
		if len(result) == 0 {
			result = json.RawMessage("{}")
		}
		// A fresh context for the outcome write: runCtx may be the very
		// deadline that killed fn. Completion is single-shot (WHERE
		// status='running' in the query): a row the sweep already failed
		// stays failed.
		recCtx, recCancel := context.WithTimeout(context.WithoutCancel(runCtx), 10*time.Second)
		defer recCancel()
		rows, cerr := st.CompleteOperation(recCtx, store.CompleteOperationParams{
			ID: op.ID, Status: status, Result: result, Error: errText,
		})
		switch {
		case cerr != nil:
			slog.ErrorContext(runCtx, "operation: record outcome",
				"kind", kind, "subject", subject, "id", op.ID, "err", cerr)
		case rows == 0:
			slog.WarnContext(runCtx, "operation finished after its deadline sweep",
				"kind", kind, "subject", subject, "id", op.ID)
		case err != nil:
			slog.WarnContext(runCtx, "operation failed",
				"kind", kind, "subject", subject, "id", op.ID, "err", err)
		default:
			slog.InfoContext(runCtx, "operation succeeded",
				"kind", kind, "subject", subject, "id", op.ID)
		}
	}()

	return op, nil
}
