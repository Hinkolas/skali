package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/metrics"
	"github.com/Hinkolas/skali/internal/pgtune"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// PoolTuner is the substrate's sizing and wake-up surface behind the
// database pool routes. *substrate.Controller implements it; nil (API-only
// mode) reports budgets as unknown and leaves a saved change for the
// substrate's next resync.
type PoolTuner interface {
	// PoolBudgets resolves every live pool's budget; ok is false while a
	// managed cluster has not observed its database nodes.
	PoolBudgets(ctx context.Context) (map[uuid.UUID]pgtune.Budget, bool, error)
	// DatabaseNodeUsable is the memory pools may share on the smallest
	// database node, the ceiling for an explicit budget.
	DatabaseNodeUsable() (node string, usable int64, ok bool)
	// EnqueuePool re-applies one pool's CNPG objects now instead of at the
	// next resync.
	EnqueuePool(id uuid.UUID)
	// PoolMembers lists a pool's live instance pods; nil without a cluster.
	PoolMembers(ctx context.Context, pool string) ([]cnpg.Instance, error)
}

type poolsHandlers struct {
	db        *dbstore.Service
	pools     PoolTuner
	reconcile *reconcile.Kernel
	// metrics serves the pool usage series; nil hides the metrics route.
	metrics *metrics.Service
	managed bool
}

type poolMemoryPayload struct {
	// Bytes is null while the automatic budget cannot be computed yet.
	Bytes  *int64 `json:"bytes"`
	Auto   bool   `json:"auto"`
	Node   string `json:"node,omitempty"`
	Capped bool   `json:"capped,omitempty"`
}

type poolParametersPayload struct {
	// Effective is the full postgresql.conf set the pool runs: derived
	// from the budget with the overrides on top. Empty while the budget is
	// unknown.
	Effective map[string]string `json:"effective"`
	Overrides map[string]string `json:"overrides"`
	// RestartKeys names the tunable parameters whose change restarts the
	// instances; the others reload live.
	RestartKeys []string `json:"restart_keys"`
	// Allowed lists every parameter an override may name.
	Allowed []string `json:"allowed"`
}

type poolObservedPayload struct {
	Phase          string `json:"phase"`
	Instances      int32  `json:"instances"`
	ReadyInstances int32  `json:"ready_instances"`
	Primary        string `json:"primary,omitempty"`
}

type poolPayload struct {
	Name         string                `json:"name"`
	Class        string                `json:"class"`
	Engine       string                `json:"engine"`
	Major        int32                 `json:"major"`
	Instances    int32                 `json:"instances"`
	StorageBytes int64                 `json:"storage_bytes"`
	Image        string                `json:"image"`
	NodePort     *int32                `json:"node_port"`
	State        string                `json:"state"`
	Memory       poolMemoryPayload     `json:"memory"`
	Parameters   poolParametersPayload `json:"parameters"`
	Observed     *poolObservedPayload  `json:"observed"`
	CreatedAt    time.Time             `json:"created_at"`
	UpdatedAt    time.Time             `json:"updated_at"`
}

// budgets resolves the live pools' budgets, or nothing when the substrate
// is absent or has not sized the nodes yet.
func (h *poolsHandlers) budgets(r *http.Request) (map[uuid.UUID]pgtune.Budget, error) {
	if h.pools == nil {
		return nil, nil
	}
	budgets, ok, err := h.pools.PoolBudgets(r.Context())
	if err != nil || !ok {
		return nil, err
	}
	return budgets, nil
}

func (h *poolsHandlers) payload(r *http.Request, pool store.DatabaseCluster, budgets map[uuid.UUID]pgtune.Budget) poolPayload {
	overrides := dbstore.ClusterParameters(pool)
	out := poolPayload{
		Name:         pool.Name,
		Class:        pool.Class,
		Engine:       pool.Engine,
		Major:        pool.Major,
		Instances:    pool.Instances,
		StorageBytes: pool.StorageBytes,
		Image:        pool.Image,
		NodePort:     pool.NodePort,
		State:        pool.State,
		Memory:       poolMemoryPayload{Auto: pool.MemoryBytes == nil},
		Parameters: poolParametersPayload{
			Effective:   map[string]string{},
			Overrides:   overrides,
			RestartKeys: pgtune.RestartKeys(),
			Allowed:     pgtune.Keys(),
		},
		CreatedAt: pool.CreatedAt,
		UpdatedAt: pool.UpdatedAt,
	}
	if budget, ok := budgets[pool.ID]; ok {
		bytes := budget.Bytes
		out.Memory = poolMemoryPayload{Bytes: &bytes, Auto: budget.Auto, Node: budget.Node, Capped: budget.Capped}
		out.Parameters.Effective = pgtune.Effective(budget.Bytes, pool.StorageBytes, h.managed, overrides)
	} else if pool.MemoryBytes != nil {
		// No substrate to size the nodes, but an explicit budget needs
		// none: report what the pool will run.
		bytes := *pool.MemoryBytes
		out.Memory = poolMemoryPayload{Bytes: &bytes}
		out.Parameters.Effective = pgtune.Effective(bytes, pool.StorageBytes, h.managed, overrides)
	}
	if h.reconcile != nil {
		if status, ok := h.reconcile.DatabaseCluster(pool.Name); ok {
			out.Observed = &poolObservedPayload{
				Phase:          status.Phase,
				Instances:      status.Instances,
				ReadyInstances: status.ReadyInstances,
				Primary:        status.Primary,
			}
		}
	}
	return out
}

// list serves every live pool with its resolved budget and effective
// parameter set.
func (h *poolsHandlers) list(w http.ResponseWriter, r *http.Request) {
	pools, err := h.db.ListLiveClusters(r.Context())
	if err != nil {
		writeInternalError(r.Context(), w, "list database pools", err)
		return
	}
	budgets, err := h.budgets(r)
	if err != nil {
		writeInternalError(r.Context(), w, "size database pools", err)
		return
	}
	payload := struct {
		Pools []poolPayload `json:"pools"`
	}{Pools: make([]poolPayload, 0, len(pools))}
	for _, pool := range pools {
		payload.Pools = append(payload.Pools, h.payload(r, pool, budgets))
	}
	writeJSON(w, http.StatusOK, payload)
}

// poolMemberPayload is one instance pod of a pool.
type poolMemberPayload struct {
	Name      string     `json:"name"`
	Role      string     `json:"role"`
	Node      string     `json:"node,omitempty"`
	Ready     bool       `json:"ready"`
	Restarts  int32      `json:"restarts"`
	StartedAt *time.Time `json:"started_at"`
	Phase     string     `json:"phase"`
}

type poolProjectRef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

type poolEnvironmentRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// poolDatabasePayload is one logical database on a pool with its owner.
// Project and environment are null for system-owned databases.
type poolDatabasePayload struct {
	DatabaseName string              `json:"database_name"`
	RoleName     string              `json:"role_name"`
	Owner        string              `json:"owner"`
	SystemKey    string              `json:"system_key,omitempty"`
	ServiceKey   string              `json:"service_key,omitempty"`
	Project      *poolProjectRef     `json:"project"`
	Environment  *poolEnvironmentRef `json:"environment"`
	Phase        string              `json:"phase"`
	StorageBytes int64               `json:"storage_bytes"`
	// UsedBytes is the newest measured logical size, null before the
	// sampler has seen the database.
	UsedBytes *int64    `json:"used_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

type poolDetailPayload struct {
	poolPayload
	Members   []poolMemberPayload   `json:"members"`
	Databases []poolDatabasePayload `json:"databases"`
}

// livePool resolves the path's pool or writes the 404; released pools
// are gone from the API's point of view.
func (h *poolsHandlers) livePool(w http.ResponseWriter, r *http.Request) (*store.DatabaseCluster, bool) {
	pool, err := h.db.LiveClusterByName(r.Context(), chi.URLParam(r, "name"))
	if errors.Is(err, dbstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "database pool not found")
		return nil, false
	}
	if err != nil {
		writeInternalError(r.Context(), w, "get database pool", err)
		return nil, false
	}
	return pool, true
}

// get serves one pool with its members (live instance pods) and every
// database on it. A failed pod read degrades to an empty member list:
// the pool's stored facts still answer.
func (h *poolsHandlers) get(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.livePool(w, r)
	if !ok {
		return
	}
	budgets, err := h.budgets(r)
	if err != nil {
		writeInternalError(r.Context(), w, "size database pools", err)
		return
	}
	payload := poolDetailPayload{
		poolPayload: h.payload(r, *pool, budgets),
		Members:     []poolMemberPayload{},
		Databases:   []poolDatabasePayload{},
	}
	if h.pools != nil {
		members, err := h.pools.PoolMembers(r.Context(), pool.Name)
		if err != nil {
			slog.WarnContext(r.Context(), "list database pool members", "pool", pool.Name, "err", err)
		}
		for _, member := range members {
			payload.Members = append(payload.Members, poolMemberPayload{
				Name:      member.Name,
				Role:      member.Role,
				Node:      member.Node,
				Ready:     member.Ready,
				Restarts:  member.Restarts,
				StartedAt: member.StartedAt,
				Phase:     member.Phase,
			})
		}
	}
	rows, err := h.db.ListClusterTenantDetails(r.Context(), pool.ID, time.Now().Add(-24*time.Hour))
	if err != nil {
		writeInternalError(r.Context(), w, "list database pool tenants", err)
		return
	}
	for _, row := range rows {
		entry := poolDatabasePayload{
			DatabaseName: row.DatabaseName,
			RoleName:     row.RoleName,
			Owner:        row.OwnerKind,
			SystemKey:    row.SystemKey,
			ServiceKey:   row.ServiceKey,
			Phase:        row.Phase,
			StorageBytes: row.StorageBytes,
			UsedBytes:    row.UsedBytes,
			CreatedAt:    row.CreatedAt,
		}
		if row.ProjectID != nil && row.ProjectName != nil {
			entry.Project = &poolProjectRef{ID: row.ProjectID.String(), Name: *row.ProjectName}
			if row.ProjectDisplayName != nil {
				entry.Project.DisplayName = *row.ProjectDisplayName
			}
		}
		if row.EnvironmentID != nil && row.EnvironmentName != nil {
			entry.Environment = &poolEnvironmentRef{ID: row.EnvironmentID.String(), Name: *row.EnvironmentName}
		}
		payload.Databases = append(payload.Databases, entry)
	}
	writeJSON(w, http.StatusOK, struct {
		Pool poolDetailPayload `json:"pool"`
	}{payload})
}

// Every series shares the timestamps array and value arrays carry null
// for buckets without samples (the console chart contract). CPU and
// memory sum the instances; the rest is the primary's exporter view.
type poolMetricsPayload struct {
	Pool           string                     `json:"pool"`
	Window         string                     `json:"window"`
	StepSeconds    int                        `json:"step_seconds"`
	Timestamps     []time.Time                `json:"timestamps"`
	CPUMillicores  []*int64                   `json:"cpu_millicores"`
	MemoryBytes    []*int64                   `json:"memory_bytes"`
	Connections    []*int64                   `json:"connections"`
	Commits        []*int64                   `json:"commits"`
	Rollbacks      []*int64                   `json:"rollbacks"`
	BlksHit        []*int64                   `json:"blks_hit"`
	BlksRead       []*int64                   `json:"blks_read"`
	CacheHitRatio  []*float64                 `json:"cache_hit_ratio"`
	DatabaseBytes  []*int64                   `json:"database_bytes"`
	InstancesReady []*int64                   `json:"instances_ready"`
	Current        *poolMetricsCurrentPayload `json:"current"`
}

// poolMetricsCurrentPayload is the newest sample plus the denominators the
// console draws against; null when the sampler has not seen the pool in
// the last minutes.
type poolMetricsCurrentPayload struct {
	SampledAt         time.Time `json:"sampled_at"`
	CPUMillicores     int64     `json:"cpu_millicores"`
	MemoryBytes       int64     `json:"memory_bytes"`
	Instances         int64     `json:"instances"`
	InstancesReady    int64     `json:"instances_ready"`
	Connections       *int64    `json:"connections"`
	DatabaseBytes     *int64    `json:"database_bytes"`
	MemoryBudgetBytes *int64    `json:"memory_budget_bytes"`
	MaxConnections    *int64    `json:"max_connections"`
}

// poolMetrics serves one pool's bucketed usage series for a window.
func (h *poolsHandlers) poolMetrics(w http.ResponseWriter, r *http.Request) {
	window, err := metrics.WindowByName(r.URL.Query().Get("window"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "unknown window; expected 1h, 24h, or 7d")
		return
	}
	pool, ok := h.livePool(w, r)
	if !ok {
		return
	}
	now := time.Now()
	series, err := h.metrics.PoolSeries(r.Context(), pool.ID, window, now)
	if err != nil {
		writeInternalError(r.Context(), w, "read database pool metrics", err)
		return
	}
	payload := poolMetricsPayload{
		Pool:           pool.Name,
		Window:         window.Name,
		StepSeconds:    int(window.Step / time.Second),
		Timestamps:     series.Timestamps,
		CPUMillicores:  series.CPUMillicores,
		MemoryBytes:    series.MemoryBytes,
		Connections:    series.Connections,
		Commits:        series.Commits,
		Rollbacks:      series.Rollbacks,
		BlksHit:        series.BlksHit,
		BlksRead:       series.BlksRead,
		CacheHitRatio:  series.CacheHitRatio,
		DatabaseBytes:  series.DatabaseBytes,
		InstancesReady: series.InstancesReady,
	}
	current, err := h.metrics.PoolCurrent(r.Context(), pool.ID, now)
	if err != nil {
		writeInternalError(r.Context(), w, "read database pool sample", err)
		return
	}
	if current != nil {
		budgets, err := h.budgets(r)
		if err != nil {
			writeInternalError(r.Context(), w, "size database pools", err)
			return
		}
		facts := h.payload(r, *pool, budgets)
		payload.Current = &poolMetricsCurrentPayload{
			SampledAt:         current.SampledAt,
			CPUMillicores:     current.CPUMillicores,
			MemoryBytes:       current.MemoryBytes,
			Instances:         current.Instances,
			InstancesReady:    current.InstancesReady,
			Connections:       current.Connections,
			DatabaseBytes:     current.DatabaseBytes,
			MemoryBudgetBytes: facts.Memory.Bytes,
		}
		if raw, ok := facts.Parameters.Effective["max_connections"]; ok {
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
				payload.Current.MaxConnections = &n
			}
		}
	}
	writeJSON(w, http.StatusOK, payload)
}

// putSettings replaces a pool's tuning. memory_bytes absent keeps the
// budget, null returns it to automatic, a number sets it; parameters, when
// present, replaces the whole override map. The substrate re-applies the
// pool right away; CNPG reloads live parameters and restarts the instances
// for the rest.
func (h *poolsHandlers) putSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MemoryBytes json.RawMessage    `json:"memory_bytes"`
		Parameters  *map[string]string `json:"parameters"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	// An absent field decodes to an empty raw message; an explicit null
	// arrives as the four bytes "null".
	memoryGiven := len(req.MemoryBytes) > 0
	if !memoryGiven && req.Parameters == nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "nothing to update: give memory_bytes, parameters, or both")
		return
	}

	pool, ok := h.livePool(w, r)
	if !ok {
		return
	}

	tuning := dbstore.ClusterTuning{MemoryBytes: pool.MemoryBytes, Parameters: dbstore.ClusterParameters(*pool)}
	before := map[string]any{"memory_bytes": tuning.MemoryBytes, "parameters": tuning.Parameters}

	var node string
	var usable int64
	nodeKnown := false
	if h.pools != nil {
		node, usable, nodeKnown = h.pools.DatabaseNodeUsable()
	}
	if memoryGiven {
		if string(req.MemoryBytes) == "null" {
			tuning.MemoryBytes = nil
		} else {
			var bytes int64
			if err := json.Unmarshal(req.MemoryBytes, &bytes); err != nil {
				writeError(w, http.StatusBadRequest, codeBadRequest, "memory_bytes must be an integer number of bytes or null")
				return
			}
			if bytes < pgtune.MinBudgetBytes {
				writeError(w, http.StatusUnprocessableEntity, codeBadRequest,
					"memory_bytes is below the minimum budget of "+pgtune.FormatMemory(pgtune.MinBudgetBytes))
				return
			}
			if nodeKnown && bytes > usable {
				message := "memory_bytes exceeds the " + pgtune.FormatMemory(usable) + " available to pools"
				if node != "" {
					message += " on the smallest database node " + node
				}
				writeError(w, http.StatusUnprocessableEntity, codeBadRequest, message)
				return
			}
			tuning.MemoryBytes = &bytes
		}
	}
	if req.Parameters != nil {
		overrides := make(map[string]string, len(*req.Parameters))
		for key, value := range *req.Parameters {
			overrides[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
		tuning.Parameters = overrides
	}

	// Validate against the budget the pool will actually run with.
	budget := int64(0)
	switch {
	case tuning.MemoryBytes != nil:
		budget = *tuning.MemoryBytes
	case !h.managed:
		budget = pgtune.DevBudgetBytes
	case nodeKnown:
		budget = pgtune.AutoBudget(pool.Class, usable)
	}
	if err := pgtune.ValidateOverrides(tuning.Parameters, budget); err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeBadRequest, err.Error())
		return
	}

	if err := h.db.SetClusterTuning(r.Context(), pool.ID, tuning); err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "database pool not found")
			return
		}
		writeInternalError(r.Context(), w, "save database pool settings", err)
		return
	}
	logAccessChange(r, "database pool settings changed", "pool", pool.Name, before,
		map[string]any{"memory_bytes": tuning.MemoryBytes, "parameters": tuning.Parameters})
	if h.pools != nil {
		h.pools.EnqueuePool(pool.ID)
	}

	updated, err := h.db.LiveClusterByName(r.Context(), pool.Name)
	if err != nil {
		writeInternalError(r.Context(), w, "reload database pool", err)
		return
	}
	budgets, err := h.budgets(r)
	if err != nil {
		writeInternalError(r.Context(), w, "size database pools", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Pool poolPayload `json:"pool"`
	}{h.payload(r, *updated, budgets)})
}
