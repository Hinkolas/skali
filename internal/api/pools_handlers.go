package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/pgtune"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/store"
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
}

type poolsHandlers struct {
	db        *dbstore.Service
	pools     PoolTuner
	reconcile *reconcile.Kernel
	managed   bool
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

	pool, err := h.db.LiveClusterByName(r.Context(), chi.URLParam(r, "name"))
	if errors.Is(err, dbstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "database pool not found")
		return
	}
	if err != nil {
		writeInternalError(r.Context(), w, "get database pool", err)
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
