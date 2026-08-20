package api

import (
	"net/http"
	"time"

	"github.com/Hinkolas/skali/internal/metrics"
)

// metricsHandlers serves bucketed usage series from stored samples. Reads
// hit only the platform database; the sampler that fills it lives in the
// daemon, so these routes answer identically in API-only mode (empty
// series).
type metricsHandlers struct {
	metrics *metrics.Service
}

// Every series in a response shares the timestamps array, and value arrays
// carry null for buckets without samples; the console chart contract
// requires exactly that alignment.
type environmentMetricsPayload struct {
	Window      string             `json:"window"`
	StepSeconds int                `json:"step_seconds"`
	Timestamps  []time.Time        `json:"timestamps"`
	Apps        []appSeriesPayload `json:"applications"`
}

type appSeriesPayload struct {
	Key           string             `json:"key"`
	CPUMillicores []*int64           `json:"cpu_millicores"`
	MemoryBytes   []*int64           `json:"memory_bytes"`
	Edge          *edgeSeriesPayload `json:"edge,omitempty"`
}

// Edge traffic through the Traefik edge only; absent until edge samples
// exist for the application.
type edgeSeriesPayload struct {
	Requests      []*int64 `json:"requests"`
	RequestBytes  []*int64 `json:"request_bytes"`
	ResponseBytes []*int64 `json:"response_bytes"`
}

type nodesMetricsPayload struct {
	Window      string              `json:"window"`
	StepSeconds int                 `json:"step_seconds"`
	Timestamps  []time.Time         `json:"timestamps"`
	Nodes       []nodeSeriesPayload `json:"nodes"`
}

type nodeSeriesPayload struct {
	Name                     string   `json:"name"`
	CPUAllocatableMillicores int64    `json:"cpu_allocatable_millicores"`
	MemoryAllocatableBytes   int64    `json:"memory_allocatable_bytes"`
	CPUMillicores            []*int64 `json:"cpu_millicores"`
	MemoryBytes              []*int64 `json:"memory_bytes"`
}

func (h *metricsHandlers) window(w http.ResponseWriter, r *http.Request) (metrics.Window, bool) {
	window, err := metrics.WindowByName(r.URL.Query().Get("window"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "unknown window; expected 1h, 24h, or 7d")
		return metrics.Window{}, false
	}
	return window, true
}

func (h *metricsHandlers) environment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	window, ok := h.window(w, r)
	if !ok {
		return
	}
	series, err := h.metrics.EnvironmentSeries(r.Context(), id, window, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "read metrics")
		return
	}
	payload := environmentMetricsPayload{
		Window:      window.Name,
		StepSeconds: int(window.Step / time.Second),
		Timestamps:  series.Timestamps,
		Apps:        []appSeriesPayload{},
	}
	for _, app := range series.Apps {
		entry := appSeriesPayload{
			Key:           app.Key,
			CPUMillicores: app.CPUMillicores,
			MemoryBytes:   app.MemoryBytes,
		}
		if app.Edge != nil {
			entry.Edge = &edgeSeriesPayload{
				Requests:      app.Edge.Requests,
				RequestBytes:  app.Edge.RequestBytes,
				ResponseBytes: app.Edge.ResponseBytes,
			}
		}
		payload.Apps = append(payload.Apps, entry)
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *metricsHandlers) nodes(w http.ResponseWriter, r *http.Request) {
	window, ok := h.window(w, r)
	if !ok {
		return
	}
	series, err := h.metrics.NodesSeries(r.Context(), window, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "read metrics")
		return
	}
	payload := nodesMetricsPayload{
		Window:      window.Name,
		StepSeconds: int(window.Step / time.Second),
		Timestamps:  series.Timestamps,
		Nodes:       []nodeSeriesPayload{},
	}
	for _, node := range series.Nodes {
		payload.Nodes = append(payload.Nodes, nodeSeriesPayload{
			Name:                     node.Name,
			CPUAllocatableMillicores: node.CPUAllocatableMillicores,
			MemoryAllocatableBytes:   node.MemoryAllocatableBytes,
			CPUMillicores:            node.CPUMillicores,
			MemoryBytes:              node.MemoryBytes,
		})
	}
	writeJSON(w, http.StatusOK, payload)
}
