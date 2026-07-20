package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/valuestore"
)

// clientStepsHandlers is the scoped write surface for run clients: the CLI
// streams its local build and import progress into the deployment run it
// opened. Writes are confined to the artifacts subtree of a running run
// owned by the same actor; the server-owned steps (validate, values,
// revision, promote, rollout) are out of reach. All log lines pass the
// same redaction as server-side writers.
type clientStepsHandlers struct {
	st      *store.Store
	journal *journal.Service
	values  *valuestore.Service
}

// maxLogLines bounds one append request.
const maxLogLines = 500

// authorizeRun admits the run's own actor while it is running.
func (h *clientStepsHandlers) authorizeRun(w http.ResponseWriter, r *http.Request, runID uuid.UUID) (*store.Run, bool) {
	run, err := h.journal.Run(r.Context(), runID)
	if err != nil {
		if errors.Is(err, journal.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return nil, false
		}
		writeInternalError(r.Context(), w, "get run", err)
		return nil, false
	}
	user := UserFrom(r.Context())
	if run.Actor != user.ID.String() {
		writeError(w, http.StatusForbidden, codeForbidden, "the run belongs to another actor")
		return nil, false
	}
	if run.Status != string(journal.RunRunning) {
		writeError(w, http.StatusConflict, codeConflict, "the run is "+run.Status)
		return nil, false
	}
	return run, true
}

func clientStepKey(key string) bool {
	return strings.HasPrefix(key, "artifacts.")
}

// POST /v1/runs/{id}/steps
func (h *clientStepsHandlers) ensureStep(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Key       string `json:"key"`
		Title     string `json:"title"`
		ParentKey string `json:"parent_key"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if !clientStepKey(req.Key) {
		writeError(w, http.StatusForbidden, codeForbidden,
			"clients may only create steps below the artifacts subtree")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "title is required")
		return
	}
	if req.ParentKey == "" || (req.ParentKey != "artifacts" && !clientStepKey(req.ParentKey)) {
		writeError(w, http.StatusBadRequest, codeBadRequest,
			"parent_key must be artifacts or one of its children")
		return
	}
	if _, ok := h.authorizeRun(w, r, id); !ok {
		return
	}
	parent, err := h.st.GetStepByRunAndKey(r.Context(), store.GetStepByRunAndKeyParams{
		RunID: id, Key: req.ParentKey,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, codeNotFound, "the parent step does not exist")
			return
		}
		writeInternalError(r.Context(), w, "get parent step", err)
		return
	}
	step, err := h.journal.EnsureStep(r.Context(), id, &parent.ID, req.Key, req.Title)
	if err != nil {
		writeInternalError(r.Context(), w, "ensure step", err)
		return
	}
	writeJSON(w, http.StatusOK, stepIdentityPayload(step))
}

// PATCH /v1/steps/{id}
func (h *clientStepsHandlers) setStepStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	target := journal.StepStatus(req.Status)
	if target != journal.StepRunning && target != journal.StepSucceeded && target != journal.StepFailed {
		writeError(w, http.StatusBadRequest, codeBadRequest, "status must be running, succeeded, or failed")
		return
	}
	step, ok := h.authorizeStep(w, r, id)
	if !ok {
		return
	}

	// A terminal client verdict first settles the running attempt so the
	// tree never shows a finished step with a live attempt.
	if target == journal.StepSucceeded || target == journal.StepFailed {
		outcome := journal.AttemptSucceeded
		if target == journal.StepFailed {
			outcome = journal.AttemptFailed
		}
		if attempt, err := h.st.GetRunningAttemptByStep(r.Context(), step.ID); err == nil {
			if err := h.journal.FinishAttempt(r.Context(), attempt.ID, outcome); err != nil &&
				!errors.Is(err, journal.ErrInvalidTransition) {
				writeInternalError(r.Context(), w, "finish attempt", err)
				return
			}
		}
	}
	if err := h.journal.SetStepStatus(r.Context(), step.ID, target); err != nil {
		if errors.Is(err, journal.ErrInvalidTransition) {
			writeError(w, http.StatusConflict, codeConflict, "invalid step status transition")
			return
		}
		writeInternalError(r.Context(), w, "set step status", err)
		return
	}
	refreshed, err := h.st.GetStepByID(r.Context(), step.ID)
	if err != nil {
		writeInternalError(r.Context(), w, "read step", err)
		return
	}
	writeJSON(w, http.StatusOK, stepIdentityPayload(&refreshed))
}

// POST /v1/steps/{id}/logs
func (h *clientStepsHandlers) appendLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Lines []struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		} `json:"lines"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxLogLines {
		writeError(w, http.StatusBadRequest, codeBadRequest, "lines must carry between 1 and 500 entries")
		return
	}
	step, ok := h.authorizeStep(w, r, id)
	if !ok {
		return
	}
	run, err := h.journal.Run(r.Context(), step.RunID)
	if err != nil {
		writeInternalError(r.Context(), w, "get run", err)
		return
	}

	// Logs imply liveness: a pending or waiting step starts running, and
	// the append lands in its running attempt.
	if step.Status == string(journal.StepPending) || step.Status == string(journal.StepWaiting) {
		if err := h.journal.SetStepStatus(r.Context(), step.ID, journal.StepRunning); err != nil {
			writeInternalError(r.Context(), w, "start step", err)
			return
		}
	}
	attempt, err := h.st.GetRunningAttemptByStep(r.Context(), step.ID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeInternalError(r.Context(), w, "get attempt", err)
			return
		}
		started, err := h.journal.StartAttempt(r.Context(), step.ID)
		if err != nil {
			writeInternalError(r.Context(), w, "start attempt", err)
			return
		}
		attempt = *started
	}

	environmentID := uuid.Nil
	if run.EnvironmentID != nil {
		environmentID = *run.EnvironmentID
	}
	candidateID := uuid.Nil
	if deployment, err := h.st.GetDeploymentByRunID(r.Context(), &run.ID); err == nil && deployment.CandidateID != nil {
		candidateID = *deployment.CandidateID
	}
	redactor, err := h.values.Redactor(r.Context(), environmentID, candidateID)
	if err != nil {
		writeInternalError(r.Context(), w, "build redactor", err)
		return
	}
	writer := h.journal.Writer(attempt.ID, redactor)
	for _, line := range req.Lines {
		level := line.Level
		switch level {
		case "debug", "info", "warn", "error":
		default:
			level = "info"
		}
		if err := writer.Log(r.Context(), level, line.Message, nil); err != nil {
			if errors.Is(err, journal.ErrAttemptTerminal) {
				writeError(w, http.StatusConflict, codeConflict, "the attempt is terminal")
				return
			}
			writeInternalError(r.Context(), w, "append log", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"appended": len(req.Lines)})
}

// authorizeStep resolves the step and applies the run authorization plus
// the artifacts-subtree confinement.
func (h *clientStepsHandlers) authorizeStep(w http.ResponseWriter, r *http.Request, stepID uuid.UUID) (*store.Step, bool) {
	step, err := h.st.GetStepByID(r.Context(), stepID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return nil, false
		}
		writeInternalError(r.Context(), w, "get step", err)
		return nil, false
	}
	if !clientStepKey(step.Key) {
		writeError(w, http.StatusForbidden, codeForbidden, "the step is server-owned")
		return nil, false
	}
	if _, ok := h.authorizeRun(w, r, step.RunID); !ok {
		return nil, false
	}
	return &step, true
}

func stepIdentityPayload(step *store.Step) any {
	return struct {
		ID     string `json:"id"`
		Key    string `json:"key"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}{step.ID.String(), step.Key, step.Title, step.Status}
}
