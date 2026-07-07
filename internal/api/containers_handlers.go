package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/cluster"
	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/store"
)

// createContainerTimeout bounds a create end-to-end, image pull included.
const createContainerTimeout = 5 * time.Minute

// containersHandlers is the admin-only raw container surface — a debug/escape
// hatch over ContainerOps, node-scoped and deliberately low-level. The
// application layer builds on ContainerOps and the node handles, not on
// these endpoints, so they can stay unstable.
type containersHandlers struct {
	st         *store.Store
	containers *cluster.ContainerOps
}

type containerStatsPayload struct {
	// CPUPct uses docker-stats semantics: 100 = one full core.
	CPUPct    float32 `json:"cpu_pct"`
	MemUsed   int64   `json:"mem_used"`
	MemLimit  int64   `json:"mem_limit"`
	NetRxRate int64   `json:"net_rx_rate"`
	NetTxRate int64   `json:"net_tx_rate"`
}

type containerPayload struct {
	ID           string                 `json:"id"`
	NodeID       uuid.UUID              `json:"node_id"`
	NodeName     string                 `json:"node_name"`
	Name         string                 `json:"name"`
	Image        string                 `json:"image"`
	Kind         string                 `json:"kind"`
	State        string                 `json:"state"`
	Health       *string                `json:"health"`
	ExitCode     *int32                 `json:"exit_code"`
	RestartCount int32                  `json:"restart_count"`
	Labels       map[string]string      `json:"labels"`
	Stats        *containerStatsPayload `json:"stats"`
	CreatedAt    *time.Time             `json:"created_at"`
	StartedAt    *time.Time             `json:"started_at"`
	FirstSeen    time.Time              `json:"first_seen"`
	LastSeen     time.Time              `json:"last_seen"`
}

func newContainerPayload(c *store.NodeContainer, nodeName string) containerPayload {
	p := containerPayload{
		ID:           c.ContainerID,
		NodeID:       c.NodeID,
		NodeName:     nodeName,
		Name:         c.Name,
		Image:        c.Image,
		Kind:         c.Kind,
		State:        c.State,
		Health:       c.Health,
		ExitCode:     c.ExitCode,
		RestartCount: c.RestartCount,
		CreatedAt:    c.ContainerCreated,
		StartedAt:    c.ContainerStarted,
		FirstSeen:    c.FirstSeen,
		LastSeen:     c.LastSeen,
	}
	if err := json.Unmarshal(c.Labels, &p.Labels); err != nil {
		p.Labels = map[string]string{}
	}
	// Stats columns are written atomically; one sentinel decides presence.
	if c.CpuPct != nil {
		p.Stats = &containerStatsPayload{
			CPUPct:    *c.CpuPct,
			MemUsed:   deref(c.MemUsed),
			MemLimit:  deref(c.MemLimit),
			NetRxRate: deref(c.NetRxRate),
			NetTxRate: deref(c.NetTxRate),
		}
	}
	return p
}

// pathContainerID reads the {cid} route param; empty is a 404 like a
// malformed node id.
func pathContainerID(w http.ResponseWriter, r *http.Request) (string, bool) {
	cid := chi.URLParam(r, "cid")
	if cid == "" {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return "", false
	}
	return cid, true
}

// requireNode resolves the {id} route param to a node, 404ing when it is
// malformed or unknown. Write handlers need the row anyway for node_name in
// the response payload.
func (h *containersHandlers) requireNode(w http.ResponseWriter, r *http.Request) (store.Node, bool) {
	id, ok := pathNodeID(w, r)
	if !ok {
		return store.Node{}, false
	}
	node, err := h.st.GetNodeByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return store.Node{}, false
		}
		writeClusterError(r.Context(), w, err)
		return store.Node{}, false
	}
	return node, true
}

// GET /v1/containers?node=
func (h *containersHandlers) list(w http.ResponseWriter, r *http.Request) {
	filter, ok := queryNodeFilter(w, r, h.st)
	if !ok {
		return
	}
	rows, err := h.st.ListContainers(r.Context(), filter)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	payload := make([]containerPayload, len(rows))
	for i := range rows {
		payload[i] = newContainerPayload(&rows[i].NodeContainer, rows[i].NodeName)
	}
	writeJSON(w, http.StatusOK, struct {
		Containers []containerPayload `json:"containers"`
	}{payload})
}

// POST /v1/nodes/{id}/containers
func (h *containersHandlers) create(w http.ResponseWriter, r *http.Request) {
	node, ok := h.requireNode(w, r)
	if !ok {
		return
	}
	id := node.ID
	var req struct {
		Name    string            `json:"name"`
		Image   string            `json:"image"`
		Kind    string            `json:"kind"`
		Env     map[string]string `json:"env"`
		Command []string          `json:"command"`
		Mounts  []struct {
			Type     string `json:"type"`
			Source   string `json:"source"`
			Target   string `json:"target"`
			ReadOnly bool   `json:"read_only"`
		} `json:"mounts"`
		Ports []struct {
			HostIP        string `json:"host_ip"`
			HostPort      uint16 `json:"host_port"`
			ContainerPort uint16 `json:"container_port"`
			Protocol      string `json:"protocol"`
		} `json:"ports"`
		RestartPolicy     string            `json:"restart_policy"`
		RestartMaxRetries int               `json:"restart_max_retries"`
		CPUs              float64           `json:"cpus"`
		MemoryLimit       int64             `json:"memory_limit"`
		Networks          []string          `json:"networks"`
		Labels            map[string]string `json:"labels"`
		Healthcheck       *struct {
			Test               []string `json:"test"`
			IntervalSeconds    int      `json:"interval_seconds"`
			TimeoutSeconds     int      `json:"timeout_seconds"`
			StartPeriodSeconds int      `json:"start_period_seconds"`
			Retries            int      `json:"retries"`
		} `json:"healthcheck"`
		Pull  string `json:"pull"`
		Start *bool  `json:"start"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	spec := engine.ContainerSpec{
		Name:              req.Name,
		Image:             req.Image,
		Env:               req.Env,
		Command:           req.Command,
		Restart:           engine.RestartPolicy(req.RestartPolicy),
		RestartMaxRetries: req.RestartMaxRetries,
		NanoCPUs:          int64(req.CPUs * 1e9),
		MemoryLimit:       req.MemoryLimit,
		Networks:          req.Networks,
		Labels:            req.Labels,
	}
	for _, m := range req.Mounts {
		spec.Mounts = append(spec.Mounts, engine.Mount{
			Type: m.Type, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly,
		})
	}
	for _, p := range req.Ports {
		spec.Ports = append(spec.Ports, engine.PortBinding{
			HostIP: p.HostIP, HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: p.Protocol,
		})
	}
	if hc := req.Healthcheck; hc != nil {
		spec.Healthcheck = &engine.Healthcheck{
			Test:        hc.Test,
			Interval:    time.Duration(hc.IntervalSeconds) * time.Second,
			Timeout:     time.Duration(hc.TimeoutSeconds) * time.Second,
			StartPeriod: time.Duration(hc.StartPeriodSeconds) * time.Second,
			Retries:     hc.Retries,
		}
	}
	start := req.Start == nil || *req.Start

	// Detached from the router's 30s ceiling: an image pull can far outlive
	// it (and the client's patience) — the pull finishes server-side either
	// way and the heartbeat records whatever it produced.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), createContainerTimeout)
	defer cancel()
	row, err := h.containers.Create(ctx, id, cluster.CreateContainerInput{
		Spec:  spec,
		Kind:  req.Kind,
		Pull:  engine.PullPolicy(req.Pull),
		Start: start,
	})
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	slog.InfoContext(r.Context(), "api: container created",
		"node_id", id, "container_id", row.ContainerID, "name", row.Name, "by", UserFrom(r.Context()).ID)
	writeJSON(w, http.StatusCreated, struct {
		Container containerPayload `json:"container"`
	}{newContainerPayload(&row, node.Name)})
}

// POST /v1/nodes/{id}/containers/{cid}/start
func (h *containersHandlers) start(w http.ResponseWriter, r *http.Request) {
	node, ok := h.requireNode(w, r)
	if !ok {
		return
	}
	cid, ok := pathContainerID(w, r)
	if !ok {
		return
	}
	row, err := h.containers.Start(r.Context(), node.ID, cid)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Container containerPayload `json:"container"`
	}{newContainerPayload(&row, node.Name)})
}

// POST /v1/nodes/{id}/containers/{cid}/stop
func (h *containersHandlers) stop(w http.ResponseWriter, r *http.Request) {
	node, ok := h.requireNode(w, r)
	if !ok {
		return
	}
	cid, ok := pathContainerID(w, r)
	if !ok {
		return
	}
	var timeout time.Duration
	if r.ContentLength > 0 {
		var req struct {
			TimeoutSeconds int `json:"timeout_seconds"`
		}
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	row, err := h.containers.Stop(r.Context(), node.ID, cid, timeout)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Container containerPayload `json:"container"`
	}{newContainerPayload(&row, node.Name)})
}

// DELETE /v1/nodes/{id}/containers/{cid}?force=true
func (h *containersHandlers) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathNodeID(w, r)
	if !ok {
		return
	}
	cid, ok := pathContainerID(w, r)
	if !ok {
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := h.containers.Remove(r.Context(), id, cid, force); err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	slog.InfoContext(r.Context(), "api: container removed",
		"node_id", id, "container_id", cid, "by", UserFrom(r.Context()).ID)
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/images?node=
func (h *containersHandlers) images(w http.ResponseWriter, r *http.Request) {
	filter, ok := queryNodeFilter(w, r, h.st)
	if !ok {
		return
	}
	rows, err := h.st.ListImages(r.Context(), filter)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	payload := make([]imagePayload, len(rows))
	for i := range rows {
		payload[i] = newImagePayload(&rows[i].NodeImage, rows[i].NodeName)
	}
	writeJSON(w, http.StatusOK, struct {
		Images []imagePayload `json:"images"`
	}{payload})
}

// GET /v1/volumes?node=
func (h *containersHandlers) volumes(w http.ResponseWriter, r *http.Request) {
	filter, ok := queryNodeFilter(w, r, h.st)
	if !ok {
		return
	}
	rows, err := h.st.ListVolumes(r.Context(), filter)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	payload := make([]volumePayload, len(rows))
	for i := range rows {
		payload[i] = newVolumePayload(&rows[i].NodeVolume, rows[i].NodeName)
	}
	writeJSON(w, http.StatusOK, struct {
		Volumes []volumePayload `json:"volumes"`
	}{payload})
}
