package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Hinkolas/skali/internal/runtimelogs"
)

// logsHandlers streams live application logs through the public API
// runtime logs never enter the system database and are
// never mixed with deployment-step logs.
type logsHandlers struct {
	logs *runtimelogs.Streamer
}

// GET /v1/environments/{id}/logs/stream?service=web
// Mounted outside the request timeout like every SSE surface.
func (h *logsHandlers) stream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, codeInternal, "streaming unsupported")
		return
	}
	service := r.URL.Query().Get("service")

	events := make(chan runtimelogs.Event, 256)
	streamCtx := r.Context()
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- h.logs.Stream(streamCtx, id, service, events)
	}()

	// Surface immediate failures (no cluster, unknown environment) as
	// proper status codes before committing to the event stream.
	select {
	case err := <-streamErr:
		switch {
		case errors.Is(err, runtimelogs.ErrNoCluster):
			writeError(w, http.StatusServiceUnavailable, codeNodeUnreachable,
				"no cluster is connected; runtime logs are unavailable")
		case errors.Is(err, runtimelogs.ErrEnvironmentNotFound):
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
		case err == nil:
			writeError(w, http.StatusServiceUnavailable, codeNodeUnreachable, "log stream ended immediately")
		default:
			writeInternalError(r.Context(), w, "runtime logs", err)
		}
		return
	case <-time.After(150 * time.Millisecond):
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-streamErr:
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case event := <-events:
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}
