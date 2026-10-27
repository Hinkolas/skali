package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/build"
	"github.com/Hinkolas/skali/internal/buildstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/valuestore"
)

// clientStepsHandlers is the scoped write surface for run clients: the CLI
// journals its local builds, uploads, and imports as steps of the
// deployment run it opened, and uploads each build's insights report.
// Writes are confined to the artifacts subtree of a running run owned by
// the same actor; the server-owned steps (validate, values, revision,
// promote, rollout) are out of reach. Uploaded text passes the same
// redaction as server-side writers.
type clientStepsHandlers struct {
	st      *store.Store
	journal *journal.Service
	values  *valuestore.Service
	builds  *buildstore.Service
}

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

// buildStepKey matches the build step of an application's artifact,
// artifacts.<application>.build.
func buildStepKey(key string) bool {
	parts := strings.Split(key, ".")
	return len(parts) == 3 && parts[0] == "artifacts" && parts[1] != "" && parts[2] == "build"
}

// clientStepService is the service a client step belongs to, which the
// step records. The deployment hands each application its subtree root,
// artifacts.<application> (the action's step_key), and an application key
// never contains a dot, so the key's second segment names it.
func clientStepService(key string) string {
	rest, ok := strings.CutPrefix(key, "artifacts.")
	if !ok {
		return ""
	}
	application, _, _ := strings.Cut(rest, ".")
	if application == "" {
		return ""
	}
	return "applications." + application
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
	step, err := h.journal.EnsureServiceStep(r.Context(), id, &parent.ID, req.Key, req.Title, clientStepService(req.Key))
	if errors.Is(err, journal.ErrRunFinished) {
		writeError(w, http.StatusConflict, codeConflict, "the run has finished")
		return
	}
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

// errDeploymentClosed refuses a build insights report whose deployment
// closed while the report was stored.
var errDeploymentClosed = errors.New("the deployment closed while the report was stored")

// PUT /v1/steps/{id}/build-insights
//
// The insights report of a local build, uploaded once after the build
// ended. The report passes the same redaction as journal writes.
func (h *clientStepsHandlers) putBuildInsights(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var report build.Insights
	if err := decodeJSONLimit(w, r, &report, build.MaxInsightsJSONBytes); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	step, ok := h.authorizeStep(w, r, id)
	if !ok {
		return
	}
	if !buildStepKey(step.Key) {
		writeError(w, http.StatusConflict, codeConflict, "the step is not a build step")
		return
	}
	run, err := h.journal.Run(r.Context(), step.RunID)
	if err != nil {
		writeInternalError(r.Context(), w, "get run", err)
		return
	}
	redactor, err := h.runRedactor(r, run)
	if err != nil {
		writeInternalError(r.Context(), w, "build redactor", err)
		return
	}
	// Closing a deployment settles its status first and only then discards
	// the staged secrets the redactor read. A deployment still preparing
	// when the report is written kept them through redaction; one that
	// closed meanwhile may not have, so its report rolls back unseen.
	guard := func(q *store.Queries) error {
		deployment, err := q.GetDeploymentByRunID(r.Context(), &run.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get deployment: %w", err)
		}
		if deployment.Status != string(deploy.DeploymentPreparing) {
			return errDeploymentClosed
		}
		return nil
	}
	if err := h.builds.PutInsights(r.Context(), step.ID, redactor, report, guard); err != nil {
		switch {
		case errors.Is(err, buildstore.ErrInvalidInsights):
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		case errors.Is(err, errDeploymentClosed):
			writeError(w, http.StatusConflict, codeConflict, err.Error())
		default:
			writeInternalError(r.Context(), w, "put build insights", err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runRedactor is the redactor of the environment and candidate a run
// deploys, which every client-written text passes. A build's output was
// written over the whole build, so it also knows the versions retired
// since.
func (h *clientStepsHandlers) runRedactor(r *http.Request, run *store.Run) (*redact.Redactor, error) {
	environmentID := uuid.Nil
	if run.EnvironmentID != nil {
		environmentID = *run.EnvironmentID
	}
	candidateID := uuid.Nil
	deployment, err := h.st.GetDeploymentByRunID(r.Context(), &run.ID)
	switch {
	case err == nil && deployment.CandidateID != nil:
		candidateID = *deployment.CandidateID
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		// Without the candidate the redactor would miss its staged secrets.
		return nil, fmt.Errorf("get deployment: %w", err)
	}
	return h.values.RetainedRedactor(r.Context(), environmentID, candidateID)
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
