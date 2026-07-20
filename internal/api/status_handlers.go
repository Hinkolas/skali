package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/reconcile"
)

// statusHandlers serves topology and health projections. Every read comes
// from the database pointers plus the in-memory observed store; no handler
// here can reach Kubernetes because none holds a cluster client.
type statusHandlers struct {
	reconcile *reconcile.Kernel
}

type revisionRefPayload struct {
	ID       string `json:"id"`
	Checksum string `json:"checksum"`
}

type observationPayload struct {
	State      string     `json:"state"`
	StaleSince *time.Time `json:"stale_since"`
	LastSync   *time.Time `json:"last_sync"`
}

type healthDiagnosticPayload struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Resource string `json:"resource,omitempty"`
}

type podPayload struct {
	Name      string     `json:"name"`
	Node      string     `json:"node"`
	Phase     string     `json:"phase"`
	Ready     bool       `json:"ready"`
	Restarts  int32      `json:"restarts"`
	Reason    string     `json:"reason,omitempty"`
	StartedAt *time.Time `json:"started_at"`
}

type serviceStatusPayload struct {
	Key         string              `json:"key"`
	Type        string              `json:"type"`
	Health      string              `json:"health"`
	Diagnostics []healthDiagnosticPayload `json:"diagnostics"`
	Pods        []podPayload        `json:"pods"`
}

type environmentStatusPayload struct {
	EnvironmentID  string                 `json:"environment_id"`
	State          string                 `json:"state"`
	TargetRevision *revisionRefPayload    `json:"target_revision"`
	ActiveRevision *revisionRefPayload    `json:"active_revision"`
	Observation    observationPayload     `json:"observation"`
	Services       []serviceStatusPayload `json:"services"`
}

func newObservationPayload(source module.SourceStatus) observationPayload {
	payload := observationPayload{State: source.State}
	if !source.StaleSince.IsZero() {
		staleSince := source.StaleSince
		payload.StaleSince = &staleSince
	}
	if !source.LastSync.IsZero() {
		lastSync := source.LastSync
		payload.LastSync = &lastSync
	}
	return payload
}

func newEnvironmentStatusPayload(status *reconcile.Status) environmentStatusPayload {
	payload := environmentStatusPayload{
		EnvironmentID: status.EnvironmentID.String(),
		State:         status.State,
		Observation:   newObservationPayload(status.Observation),
		Services:      make([]serviceStatusPayload, 0, len(status.Services)),
	}
	if status.Target != nil {
		payload.TargetRevision = &revisionRefPayload{ID: status.Target.ID.String(), Checksum: status.Target.Checksum}
	}
	if status.Active != nil {
		payload.ActiveRevision = &revisionRefPayload{ID: status.Active.ID.String(), Checksum: status.Active.Checksum}
	}
	for _, service := range status.Services {
		servicePayload := serviceStatusPayload{
			Key:         service.Key,
			Type:        service.Type,
			Health:      string(service.Health),
			Diagnostics: make([]healthDiagnosticPayload, 0, len(service.Diagnostics)),
			Pods:        make([]podPayload, 0, len(service.Pods)),
		}
		for _, diagnostic := range service.Diagnostics {
			servicePayload.Diagnostics = append(servicePayload.Diagnostics, healthDiagnosticPayload{
				Severity: diagnostic.Severity,
				Code:     diagnostic.Code,
				Message:  diagnostic.Message,
				Resource: diagnostic.Resource,
			})
		}
		for _, pod := range service.Pods {
			podEntry := podPayload{
				Name:     pod.Name,
				Node:     pod.Node,
				Phase:    pod.Phase,
				Ready:    pod.Ready,
				Restarts: pod.Restarts,
				Reason:   pod.Reason,
			}
			if !pod.Started.IsZero() {
				started := pod.Started
				podEntry.StartedAt = &started
			}
			servicePayload.Pods = append(servicePayload.Pods, podEntry)
		}
		payload.Services = append(payload.Services, servicePayload)
	}
	return payload
}

func (h *statusHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	status, err := h.reconcile.Status(r.Context(), id)
	if err != nil {
		writeStatusError(r, w, err)
		return
	}
	writeJSON(w, http.StatusOK, newEnvironmentStatusPayload(status))
}

// stream is the projection invalidation stream made client-visible: the
// full status document is re-sent, coalesced, on every invalidation that
// touches the environment. Mounted outside the request timeout.
func (h *statusHandlers) stream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, codeInternal, "streaming unsupported")
		return
	}
	status, err := h.reconcile.Status(r.Context(), id)
	if err != nil {
		writeStatusError(r, w, err)
		return
	}
	invalidations, cancel := h.reconcile.SubscribeStatus(id)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(status *reconcile.Status) bool {
		data, err := json.Marshal(newEnvironmentStatusPayload(status))
		if err != nil {
			return false
		}
		fmt.Fprintf(w, "event: status\ndata: %s\n\n", data)
		flusher.Flush()
		return true
	}
	if !send(status) {
		return
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
		case _, open := <-invalidations:
			if !open {
				// Fell behind; the client reconnects and re-reads.
				return
			}
			// Coalesce whatever queued up; the payload is a full document.
			for {
				select {
				case _, more := <-invalidations:
					if !more {
						return
					}
					continue
				default:
				}
				break
			}
			status, err := h.reconcile.Status(r.Context(), id)
			if err != nil {
				return
			}
			if !send(status) {
				return
			}
		}
	}
}

type kindSyncPayload struct {
	Kind   string `json:"kind"`
	Synced bool   `json:"synced"`
}

type systemObservationPayload struct {
	Mode        string             `json:"mode"`
	Ready       bool               `json:"ready"`
	Observation observationPayload `json:"observation"`
	Kinds       []kindSyncPayload  `json:"kinds"`
	QueueDepth  int                `json:"queue_depth"`
	Workers     int                `json:"workers"`
}

func (h *statusHandlers) system(w http.ResponseWriter, r *http.Request) {
	info := h.reconcile.Observation()
	payload := systemObservationPayload{
		Mode:        info.Mode,
		Ready:       info.Ready,
		Observation: newObservationPayload(info.Source),
		Kinds:       make([]kindSyncPayload, 0, len(info.Kinds)),
		QueueDepth:  info.QueueDepth,
		Workers:     info.Workers,
	}
	for _, kind := range info.Kinds {
		payload.Kinds = append(payload.Kinds, kindSyncPayload{Kind: kind.Kind, Synced: kind.Synced})
	}
	writeJSON(w, http.StatusOK, payload)
}

func writeStatusError(r *http.Request, w http.ResponseWriter, err error) {
	if errors.Is(err, reconcile.ErrEnvironmentNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "environment not found")
		return
	}
	writeInternalError(r.Context(), w, "environment status", err)
}
