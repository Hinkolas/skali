package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/cluster"
	"github.com/Hinkolas/skali/internal/store"
)

// nodesHandlers is the admin-only node-management surface (the router wraps
// every route in RequireAdmin; writes additionally sit behind RequireFresh).
// Cluster invariants (master-role immutability, master-node protection) live
// in internal/cluster.
type nodesHandlers struct {
	st      *store.Store
	cluster *cluster.Service
}

type nodePayload struct {
	ID            uuid.UUID           `json:"id"`
	Name          string              `json:"name"`
	Roles         []string            `json:"roles"`
	AdvertiseAddr string              `json:"advertise_addr"`
	PublicAddr    *string             `json:"public_addr"`
	Arch          *string             `json:"arch"`
	OS            *string             `json:"os"`
	SkalidVersion *string             `json:"skalid_version"`
	Status        string              `json:"status"`
	LastSeen      *time.Time          `json:"last_seen"`
	Metrics       *nodeMetricsPayload `json:"metrics"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

// nodeMetricsPayload is the latest resource snapshot; rates are bytes/second,
// cpu_pct is 0–100 across effective cores.
type nodeMetricsPayload struct {
	CPUPct        float32 `json:"cpu_pct"`
	MemUsed       int64   `json:"mem_used"`
	MemTotal      int64   `json:"mem_total"`
	DiskUsed      int64   `json:"disk_used"`
	DiskTotal     int64   `json:"disk_total"`
	NetRxRate     int64   `json:"net_rx_rate"`
	NetTxRate     int64   `json:"net_tx_rate"`
	DiskReadRate  int64   `json:"disk_read_rate"`
	DiskWriteRate int64   `json:"disk_write_rate"`
	Load1         float32 `json:"load1"`
}

func newNodePayload(n *store.Node) nodePayload {
	p := nodePayload{
		ID:            n.ID,
		Name:          n.Name,
		Roles:         n.Roles,
		AdvertiseAddr: n.AdvertiseAddr,
		PublicAddr:    n.PublicAddr,
		Arch:          n.Arch,
		OS:            n.Os,
		SkalidVersion: n.SkalidVersion,
		Status:        n.Status,
		LastSeen:      n.LastSeen,
		CreatedAt:     n.CreatedAt,
		UpdatedAt:     n.UpdatedAt,
	}
	// The poller writes all metric columns atomically, so one sentinel column
	// decides presence.
	if n.CpuPct != nil {
		p.Metrics = &nodeMetricsPayload{
			CPUPct:        *n.CpuPct,
			MemUsed:       deref(n.MemUsed),
			MemTotal:      deref(n.MemTotal),
			DiskUsed:      deref(n.DiskUsed),
			DiskTotal:     deref(n.DiskTotal),
			NetRxRate:     deref(n.NetRxRate),
			NetTxRate:     deref(n.NetTxRate),
			DiskReadRate:  deref(n.DiskReadRate),
			DiskWriteRate: deref(n.DiskWriteRate),
			Load1:         deref(n.Load1),
		}
	}
	return p
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// pathNodeID parses the {id} route param, writing a 404 on malformed ids so
// they are indistinguishable from missing nodes.
func pathNodeID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return uuid.UUID{}, false
	}
	return id, true
}

// GET /v1/nodes
func (h *nodesHandlers) list(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.st.ListNodes(r.Context())
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	payload := make([]nodePayload, len(nodes))
	for i := range nodes {
		payload[i] = newNodePayload(&nodes[i])
	}
	writeJSON(w, http.StatusOK, struct {
		Nodes []nodePayload `json:"nodes"`
	}{payload})
}

// POST /v1/nodes/tokens
func (h *nodesHandlers) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Roles []string `json:"roles"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if len(req.Roles) == 0 {
		req.Roles = []string{"worker"}
	}

	result, err := h.cluster.CreateJoinToken(r.Context(), req.Roles, UserFrom(r.Context()).ID)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Token         string    `json:"token"`
		ExpiresAt     time.Time `json:"expires_at"`
		EnrollCommand string    `json:"enroll_command"`
	}{result.Token, result.ExpiresAt, result.EnrollCommand})
}

// nodeMetricSamplePayload is one bucketed history point (2-minute average).
type nodeMetricSamplePayload struct {
	SampledAt time.Time `json:"sampled_at"`
	nodeMetricsPayload
}

// GET /v1/nodes/{id}/metrics
func (h *nodesHandlers) metrics(w http.ResponseWriter, r *http.Request) {
	id, ok := pathNodeID(w, r)
	if !ok {
		return
	}
	if _, err := h.st.GetNodeByID(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return
		}
		writeClusterError(r.Context(), w, err)
		return
	}
	rows, err := h.st.ListNodeMetricsBucketed(r.Context(), id)
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	samples := make([]nodeMetricSamplePayload, len(rows))
	for i, row := range rows {
		samples[i] = nodeMetricSamplePayload{
			SampledAt: row.Bucket,
			nodeMetricsPayload: nodeMetricsPayload{
				CPUPct:        row.CpuPct,
				MemUsed:       row.MemUsed,
				MemTotal:      row.MemTotal,
				DiskUsed:      row.DiskUsed,
				DiskTotal:     row.DiskTotal,
				NetRxRate:     row.NetRxRate,
				NetTxRate:     row.NetTxRate,
				DiskReadRate:  row.DiskReadRate,
				DiskWriteRate: row.DiskWriteRate,
				Load1:         row.Load1,
			},
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Samples []nodeMetricSamplePayload `json:"samples"`
	}{samples})
}

// PATCH /v1/nodes/{id}
func (h *nodesHandlers) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathNodeID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name       *string  `json:"name"`
		Roles      []string `json:"roles"`
		PublicAddr *string  `json:"public_addr"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if req.Name == nil && req.Roles == nil && req.PublicAddr == nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "nothing to update: provide name, roles and/or public_addr")
		return
	}

	node, err := h.cluster.UpdateNode(r.Context(), id, cluster.NodeUpdate{
		Name:       req.Name,
		Roles:      req.Roles,
		PublicAddr: req.PublicAddr,
	})
	if err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Node nodePayload `json:"node"`
	}{newNodePayload(&node)})
}

// DELETE /v1/nodes/{id}
func (h *nodesHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathNodeID(w, r)
	if !ok {
		return
	}
	if err := h.cluster.DeleteNode(r.Context(), id); err != nil {
		writeClusterError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeClusterError maps cluster sentinel errors onto the envelope; anything
// unrecognized is logged and reported as an opaque 500.
func writeClusterError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cluster.ErrNodeNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, cluster.ErrMasterNode):
		writeError(w, http.StatusConflict, codeConflict, "the master node's role cannot be changed or removed")
	case errors.Is(err, cluster.ErrClusterAddrUnset):
		writeError(w, http.StatusConflict, codeConflict, "CLUSTER_ADDR is not configured on the master; set it to the externally reachable gRPC address")
	case errors.Is(err, cluster.ErrInvalidRole):
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
	case errors.Is(err, cluster.ErrContainerNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, cluster.ErrContainerConflict):
		writeError(w, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, cluster.ErrInvalidSpec):
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
	case errors.Is(err, cluster.ErrNodeUnreachable), errors.Is(err, cluster.ErrEngineUnavailable):
		writeError(w, http.StatusBadGateway, codeNodeUnreachable, err.Error())
	default:
		slog.ErrorContext(ctx, "api: internal error", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}
