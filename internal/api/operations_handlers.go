package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/operations"
	"github.com/Hinkolas/skali/internal/store"
)

// operationsListCap bounds the list response; operations are pruned after a
// day, so this is generous.
const operationsListCap = 100

// operationsHandlers is the read side of task-shaped background work:
// clients poll here after a 202.
type operationsHandlers struct {
	st *store.Store
}

type operationPayload struct {
	ID         uuid.UUID       `json:"id"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	Subject    string          `json:"subject"`
	Result     json.RawMessage `json:"result"`
	Error      *string         `json:"error,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

func newOperationPayload(op *store.Operation) operationPayload {
	return operationPayload{
		ID:         op.ID,
		Kind:       op.Kind,
		Status:     op.Status,
		Subject:    op.Subject,
		Result:     json.RawMessage(op.Result),
		Error:      op.Error,
		CreatedAt:  op.CreatedAt,
		UpdatedAt:  op.UpdatedAt,
		FinishedAt: op.FinishedAt,
	}
}

func (h *operationsHandlers) list(w http.ResponseWriter, r *http.Request) {
	var kind, status *string
	if v := r.URL.Query().Get("kind"); v != "" {
		kind = &v
	}
	if v := r.URL.Query().Get("status"); v != "" {
		if v != operations.StatusRunning && v != operations.StatusSucceeded && v != operations.StatusFailed {
			writeError(w, http.StatusBadRequest, codeBadRequest, "status must be running, succeeded, or failed")
			return
		}
		status = &v
	}
	rows, err := h.st.ListOperations(r.Context(), store.ListOperationsParams{
		Kind: kind, Status: status, Limit: operationsListCap,
	})
	if err != nil {
		writeInternalError(r.Context(), w, "list operations", err)
		return
	}
	ops := make([]operationPayload, 0, len(rows))
	for i := range rows {
		ops = append(ops, newOperationPayload(&rows[i]))
	}
	writeJSON(w, http.StatusOK, struct {
		Operations []operationPayload `json:"operations"`
	}{ops})
}

func (h *operationsHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	op, err := h.st.GetOperationByID(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	if err != nil {
		writeInternalError(r.Context(), w, "get operation", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Operation operationPayload `json:"operation"`
	}{newOperationPayload(&op)})
}
