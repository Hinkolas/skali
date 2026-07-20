package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
)

// runsHandlers is the read surface over the run journal: run lists, nested
// run trees, and step logs (paged and streamed). The journal explains
// reconciliation but never drives it, so this surface is read-only.
type runsHandlers struct {
	journal *journal.Service
}

type runPayload struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	ProjectID     *string    `json:"project_id"`
	EnvironmentID *string    `json:"environment_id"`
	Actor         string     `json:"actor"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
}

func newRunPayload(r *store.Run) runPayload {
	payload := runPayload{
		ID:         r.ID.String(),
		Kind:       r.Kind,
		Actor:      r.Actor,
		Status:     r.Status,
		CreatedAt:  r.CreatedAt,
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
	}
	if r.ProjectID != nil {
		id := r.ProjectID.String()
		payload.ProjectID = &id
	}
	if r.EnvironmentID != nil {
		id := r.EnvironmentID.String()
		payload.EnvironmentID = &id
	}
	return payload
}

type attemptPayload struct {
	ID         string     `json:"id"`
	Number     int64      `json:"number"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

type stepPayload struct {
	ID              string           `json:"id"`
	Key             string           `json:"key"`
	Title           string           `json:"title"`
	Status          string           `json:"status"`
	ProgressCurrent *int64           `json:"progress_current,omitempty"`
	ProgressTotal   *int64           `json:"progress_total,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	StartedAt       *time.Time       `json:"started_at"`
	FinishedAt      *time.Time       `json:"finished_at"`
	Attempts        []attemptPayload `json:"attempts"`
	Children        []stepPayload    `json:"children,omitempty"`
}

func newStepPayload(node *journal.TreeStep) stepPayload {
	payload := stepPayload{
		ID:              node.Step.ID.String(),
		Key:             node.Step.Key,
		Title:           node.Step.Title,
		Status:          node.Step.Status,
		ProgressCurrent: node.Step.ProgressCurrent,
		ProgressTotal:   node.Step.ProgressTotal,
		CreatedAt:       node.Step.CreatedAt,
		StartedAt:       node.Step.StartedAt,
		FinishedAt:      node.Step.FinishedAt,
		Attempts:        make([]attemptPayload, len(node.Attempts)),
	}
	for i, attempt := range node.Attempts {
		payload.Attempts[i] = attemptPayload{
			ID:         attempt.ID.String(),
			Number:     attempt.Number,
			Status:     attempt.Status,
			StartedAt:  attempt.StartedAt,
			FinishedAt: attempt.FinishedAt,
		}
	}
	for _, child := range node.Children {
		payload.Children = append(payload.Children, newStepPayload(child))
	}
	return payload
}

type logPayload struct {
	Attempt int64           `json:"attempt"`
	Seq     int64           `json:"seq"`
	TS      time.Time       `json:"ts"`
	Level   string          `json:"level"`
	Message string          `json:"message"`
	Fields  json.RawMessage `json:"fields"`
}

func newLogPayload(event journal.LogEvent) logPayload {
	fields := json.RawMessage(event.Fields)
	if len(fields) == 0 {
		fields = json.RawMessage("{}")
	}
	return logPayload{
		Attempt: event.AttemptNumber,
		Seq:     event.Seq,
		TS:      event.TS,
		Level:   event.Level,
		Message: event.Message,
		Fields:  fields,
	}
}

// cursorString renders the composite step-log cursor "attempt:seq".
func cursorString(event journal.LogEvent) string {
	return strconv.FormatInt(event.AttemptNumber, 10) + ":" + strconv.FormatInt(event.Seq, 10)
}

// parseCursor parses "attempt:seq"; the zero cursor means "from the start".
func parseCursor(raw string) (journal.Cursor, error) {
	if raw == "" {
		return journal.Cursor{}, nil
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return journal.Cursor{}, errors.New("cursor must be attempt:seq")
	}
	attempt, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return journal.Cursor{}, errors.New("cursor must be attempt:seq")
	}
	seq, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return journal.Cursor{}, errors.New("cursor must be attempt:seq")
	}
	return journal.Cursor{AttemptNumber: attempt, Seq: seq}, nil
}

func writeJournalError(r *http.Request, w http.ResponseWriter, err error) {
	if errors.Is(err, journal.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	writeInternalError(r.Context(), w, "journal error", err)
}

// GET /v1/environments/{id}/runs
func (h *runsHandlers) list(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	runs, err := h.journal.ListRuns(r.Context(), id)
	if err != nil {
		writeJournalError(r, w, err)
		return
	}
	payload := make([]runPayload, len(runs))
	for i := range runs {
		payload[i] = newRunPayload(&runs[i])
	}
	writeJSON(w, http.StatusOK, struct {
		Runs []runPayload `json:"runs"`
	}{payload})
}

// GET /v1/runs/{id}
func (h *runsHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tree, err := h.journal.RunTree(r.Context(), id)
	if err != nil {
		writeJournalError(r, w, err)
		return
	}
	steps := make([]stepPayload, len(tree.Steps))
	for i, node := range tree.Steps {
		steps[i] = newStepPayload(node)
	}
	writeJSON(w, http.StatusOK, struct {
		Run   runPayload    `json:"run"`
		Steps []stepPayload `json:"steps"`
	}{newRunPayload(&tree.Run), steps})
}

// GET /v1/steps/{id}/logs
func (h *runsHandlers) stepLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	after, err := parseCursor(r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, "limit must be an integer")
			return
		}
	}
	events, err := h.journal.StepLogs(r.Context(), id, after, limit)
	if err != nil {
		writeJournalError(r, w, err)
		return
	}
	payload := make([]logPayload, len(events))
	next := ""
	for i, event := range events {
		payload[i] = newLogPayload(event)
		next = cursorString(event)
	}
	writeJSON(w, http.StatusOK, struct {
		Logs []logPayload `json:"logs"`
		Next string       `json:"next,omitempty"`
	}{payload, next})
}

// GET /v1/steps/{id}/logs/stream (SSE)
//
// Streams step-log entries as they are committed. The event id is the
// composite cursor "attempt:seq"; reconnecting clients resume via the
// standard Last-Event-ID header (or an initial ?after=). Mounted outside
// the request-timeout middleware.
func (h *runsHandlers) streamLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, codeInternal, "streaming unsupported")
		return
	}
	after, err := parseCursor(r.Header.Get("Last-Event-ID"))
	if err == nil && r.Header.Get("Last-Event-ID") == "" {
		after, err = parseCursor(r.URL.Query().Get("after"))
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	subscription, err := h.journal.Subscribe(r.Context(), id, after)
	if err != nil {
		writeJournalError(r, w, err)
		return
	}
	defer subscription.Cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event journal.LogEvent) bool {
		data, err := json.Marshal(newLogPayload(event))
		if err != nil {
			return false
		}
		fmt.Fprintf(w, "id: %s\nevent: log\ndata: %s\n\n", cursorString(event), data)
		flusher.Flush()
		return true
	}

	cursor := after
	for _, event := range subscription.Backlog {
		if !cursor.Before(journal.Cursor{AttemptNumber: event.AttemptNumber, Seq: event.Seq}) {
			continue
		}
		if !send(event) {
			return
		}
		cursor = journal.Cursor{AttemptNumber: event.AttemptNumber, Seq: event.Seq}
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case event, open := <-subscription.Events:
			if !open {
				// The subscriber fell behind and was disconnected; the
				// client reconnects with its Last-Event-ID and catches up.
				return
			}
			eventCursor := journal.Cursor{AttemptNumber: event.AttemptNumber, Seq: event.Seq}
			if !cursor.Before(eventCursor) {
				continue // already delivered through the backlog
			}
			if !send(event) {
				return
			}
			cursor = eventCursor
		}
	}
}
