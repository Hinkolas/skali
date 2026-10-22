package substrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// The connection check: every interval, each live pool gets one fresh
// login as an ordinary role, the way an application connects, and one
// query over it. A ready pod, or a query on a connection that is already
// open, says nothing about whether the next application login succeeds:
// once the ordinary slots are taken, the server refuses every new one
// while the instances stay ready and superusers still get in.
//
// A verdict changes only after the same answer came back
// connectionConfirmations times in a row, so a pool starts or stops
// refusing within two intervals of it happening (about 30 seconds, bounded
// by the check timeout on top) and one slow check never flips health. The
// check costs each pool one connection for the length of a query; it runs
// inside the primary instance over the local socket, so it needs neither a
// password nor a network path, and keeps working while the pool's other
// consumers are starved.
const (
	ConnectionCheckInterval = 15 * time.Second
	// ConnectionCheckStale turns the connection source stale when no check
	// round completed for this long; the database module then reports
	// unknown instead of the last verdict.
	ConnectionCheckStale = 45 * time.Second
	// ConnectionCheckTimeout bounds one round over every pool; the pools
	// are checked in parallel, each bounded by connectionExecTimeout.
	ConnectionCheckTimeout  = 10 * time.Second
	connectionExecTimeout   = 8 * time.Second
	connectionConfirmations = 2
	// connectionSaturationPercent is the share of the ordinary limit in use
	// at which a pool reads as saturated: still serving, close to refusing.
	connectionSaturationPercent = 90
)

// ConnectionIncidentCode is the incident code of a pool refusing new
// connections for lack of slots (SQLSTATE 53300).
const ConnectionIncidentCode = "connections-exhausted"

// connectionReading is one check's answer for one pool.
type connectionReading struct {
	state  string
	reason string
	usage  *cnpg.ProbeUsage
}

// poolConnections is the monitor's memory of one pool: the confirmed
// verdict it publishes, the answer waiting for confirmation, and the open
// incident.
type poolConnections struct {
	current      module.DatabaseConnectionsStatus
	pending      string
	pendingCount int
	incident     *uuid.UUID
}

// connectionMonitor holds the verdicts between rounds. The poll source
// runs its probe from one goroutine; the mutex keeps a direct call (tests,
// a second caller) from interleaving with it.
type connectionMonitor struct {
	c      *Controller
	mu     sync.Mutex
	loaded bool
	pools  map[uuid.UUID]*poolConnections
	now    func() time.Time
}

// ConnectionProbe returns the connection observer's probe: one round of
// checks over every live pool, published as one platform-scoped object per
// pool under the pool's shared key. Incidents persist across restarts: a
// pool with an open incident starts out refusing and closes it only after
// confirmed successful logins.
func (c *Controller) ConnectionProbe() observe.Probe {
	return newConnectionMonitor(c, time.Now).round
}

func newConnectionMonitor(c *Controller, now func() time.Time) *connectionMonitor {
	return &connectionMonitor{c: c, pools: map[uuid.UUID]*poolConnections{}, now: now}
}

func (m *connectionMonitor) round(ctx context.Context) ([]observe.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.loaded {
		if err := m.loadIncidents(ctx); err != nil {
			return nil, err
		}
		m.loaded = true
	}
	pools, err := m.c.deps.DB.ListLiveClusters(ctx)
	if err != nil {
		return nil, err
	}
	active := make([]store.DatabaseCluster, 0, len(pools))
	for _, pool := range pools {
		if pool.State == dbstore.StateActive {
			active = append(active, pool)
		}
	}
	readings := make([]connectionReading, len(active))
	var wg sync.WaitGroup
	for i := range active {
		wg.Go(func() { readings[i] = m.c.checkConnections(ctx, active[i]) })
	}
	wg.Wait()

	now := m.now()
	live := make(map[uuid.UUID]bool, len(active))
	objects := make([]observe.Object, 0, len(active))
	for i, pool := range active {
		live[pool.ID] = true
		state := m.pools[pool.ID]
		if state == nil {
			state = &poolConnections{}
			m.pools[pool.ID] = state
		}
		if state.observe(readings[i], now) {
			slog.InfoContext(ctx, "substrate: pool connection check changed", "pool", pool.Name,
				"state", state.current.State, "reason", state.current.Reason)
		}
		m.recordIncident(ctx, pool, state, now)
		objects = append(objects, observe.DatabaseConnectionsObject(pool.Name, state.current))
	}
	for id := range m.pools {
		if !live[id] {
			delete(m.pools, id)
		}
	}
	return objects, nil
}

// loadIncidents seeds the pools with an open incident as refusing, so a
// restart neither forgets the incident nor recovers it on one login.
func (m *connectionMonitor) loadIncidents(ctx context.Context) error {
	incidents, err := m.c.deps.DB.OpenConnectionIncidents(ctx)
	if err != nil {
		return err
	}
	for _, incident := range incidents {
		id := incident.ID
		m.pools[incident.ClusterID] = &poolConnections{
			current: module.DatabaseConnectionsStatus{
				State: module.ConnectionsRefusing, Reason: incident.Message, Since: incident.OpenedAt,
			},
			incident: &id,
		}
	}
	return nil
}

// observe folds one reading into the pool's verdict and reports whether the
// confirmed state changed. The first reading of a pool is taken at once:
// there is no earlier verdict to protect. Usage follows every successful
// check; a failed one keeps the last usage with its check time, so its age
// shows.
func (p *poolConnections) observe(reading connectionReading, now time.Time) bool {
	if reading.usage != nil {
		usage := *reading.usage
		p.current.CheckedAt = now
		p.current.Used, p.current.Max, p.current.Limit = usage.Used, usage.Max, usage.Limit()
		p.current.Databases = usage.Databases
	}
	switch {
	case p.current.State == "":
		p.pending, p.pendingCount = reading.state, connectionConfirmations
	case reading.state == p.current.State:
		p.pending, p.pendingCount = "", 0
		return false
	case reading.state == p.pending:
		p.pendingCount++
	default:
		p.pending, p.pendingCount = reading.state, 1
	}
	if p.pendingCount < connectionConfirmations {
		return false
	}
	p.current.State, p.current.Reason, p.current.Since = reading.state, reading.reason, now
	p.pending, p.pendingCount = "", 0
	return true
}

// recordIncident keeps the incident table in step with the verdict: open
// on a confirmed refusal, close on a confirmed successful login. An
// unknown verdict leaves an open incident open: a check that cannot run
// proves no recovery. A failed write is retried on the next round.
func (m *connectionMonitor) recordIncident(ctx context.Context, pool store.DatabaseCluster, state *poolConnections, now time.Time) {
	switch state.current.State {
	case module.ConnectionsRefusing:
		if state.incident != nil {
			return
		}
		id, err := m.c.deps.DB.OpenConnectionIncident(ctx, pool.ID, ConnectionIncidentCode, state.current.Reason, state.current.Since)
		if err != nil {
			slog.WarnContext(ctx, "substrate: record pool connection incident", "pool", pool.Name, "err", err)
			return
		}
		state.incident = &id
		slog.WarnContext(ctx, "substrate: pool refuses new connections", "pool", pool.Name,
			"incident", id, "reason", state.current.Reason)
	case module.ConnectionsAvailable, module.ConnectionsSaturated:
		if state.incident == nil {
			return
		}
		if err := m.c.deps.DB.CloseConnectionIncident(ctx, pool.ID, now); err != nil {
			slog.WarnContext(ctx, "substrate: close pool connection incident", "pool", pool.Name, "err", err)
			return
		}
		slog.InfoContext(ctx, "substrate: pool accepts connections again", "pool", pool.Name, "incident", *state.incident)
		state.incident = nil
	}
}

// checkConnections runs one check in the pool's primary and classifies the
// answer. Only the server's slot refusal reads as refusing; everything
// else that keeps the check from logging in is unknown, never healthy.
func (c *Controller) checkConnections(ctx context.Context, pool store.DatabaseCluster) connectionReading {
	ctx, cancel := context.WithTimeout(ctx, connectionExecTimeout)
	defer cancel()
	out, err := c.deps.Cluster.ExecInPod(ctx, Namespace, cnpg.PrimarySelector(pool.Name), cnpg.PostgresContainer,
		cnpg.ProbeCommand())
	return classifyConnections(out, err, ctx.Err())
}

func classifyConnections(out string, err, ctxErr error) connectionReading {
	unknown := func(reason string) connectionReading {
		return connectionReading{state: module.ConnectionsUnknown, reason: reason}
	}
	if err != nil {
		if refusal := cnpg.ProbeRefusal(err); refusal != "" {
			return connectionReading{state: module.ConnectionsRefusing, reason: refusal}
		}
		switch {
		case errors.Is(err, kube.ErrNoReadyPod):
			return unknown("no ready primary instance to check")
		case errors.Is(ctxErr, context.DeadlineExceeded):
			return unknown(fmt.Sprintf("the connection check did not answer within %s", connectionExecTimeout))
		}
		if text := err.Error(); strings.Contains(text, `role "`+cnpg.ProbeRole+`" does not exist`) ||
			strings.Contains(text, `authentication failed for user "`+cnpg.ProbeRole+`"`) {
			// A pool applied before the check existed gets its role and
			// ident line on its next converge.
			return unknown("the connection check's role is not set up on the pool yet")
		}
		return unknown("the connection check failed: " + execFailure(err))
	}
	usage, err := cnpg.ParseProbe(out)
	if err != nil {
		return unknown(err.Error())
	}
	reading := connectionReading{state: module.ConnectionsAvailable, usage: &usage}
	if limit := usage.Limit(); limit > 0 && int64(usage.Used)*100 >= int64(limit)*connectionSaturationPercent {
		reading.state = module.ConnectionsSaturated
		reading.reason = fmt.Sprintf("%d%% or more of the connections applications may open are in use", connectionSaturationPercent)
	}
	return reading
}

// execFailure keeps the last line of a failed exec's stderr, which the
// exec error carries in a trailing "(stderr: ...)", bounded for a reason
// string. Without stderr the whole error stands.
func execFailure(err error) string {
	text := strings.TrimSpace(err.Error())
	if i := strings.Index(text, "(stderr: "); i >= 0 {
		if stderr := strings.TrimSpace(strings.TrimSuffix(text[i+len("(stderr: "):], ")")); stderr != "" {
			text = stderr
		}
	}
	if i := strings.LastIndex(text, "\n"); i >= 0 {
		text = strings.TrimSpace(text[i+1:])
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
