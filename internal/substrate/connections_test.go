package substrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/testdb"
)

// refusedExec is the exec error a check gets while the pool's ordinary
// slots are taken, as kube.ExecInPod wraps psql's stderr.
var refusedExec = errors.New(`kube: exec in skali-platform/pg17-shared-1: command terminated with exit code 2 ` +
	`(stderr: psql: error: connection to server on socket "/controller/run/.s.PGSQL.5432" failed: ` +
	"FATAL:  remaining connection slots are reserved for roles with the SUPERUSER attribute\n)")

func usageJSON(used, max int) string {
	return fmt.Sprintf(`{"max" : %d, "reserved" : 3, "used" : %d, "databases" : {"app" : %d}}`, max, used, used)
}

func TestClassifyConnections(t *testing.T) {
	t.Parallel()
	available := classifyConnections(usageJSON(12, 100), nil, nil)
	require.Equal(t, module.ConnectionsAvailable, available.state)
	require.Equal(t, &cnpg.ProbeUsage{Max: 100, Reserved: 3, Used: 12, Databases: map[string]int32{"app": 12}}, available.usage)
	require.Equal(t, int32(97), available.usage.Limit())

	// 90% of the 97 ordinary slots is 87.3: 87 still reads available, 88
	// does not.
	require.Equal(t, module.ConnectionsAvailable, classifyConnections(usageJSON(87, 100), nil, nil).state)
	saturated := classifyConnections(usageJSON(88, 100), nil, nil)
	require.Equal(t, module.ConnectionsSaturated, saturated.state)
	require.NotNil(t, saturated.usage, "a saturated pool still reports its usage")

	refusing := classifyConnections("", refusedExec, nil)
	require.Equal(t, connectionReading{
		state:  module.ConnectionsRefusing,
		reason: "remaining connection slots are reserved for roles with the SUPERUSER attribute",
	}, refusing)
	tooMany := classifyConnections("", errors.New("FATAL:  sorry, too many clients already"), nil)
	require.Equal(t, module.ConnectionsRefusing, tooMany.state)
	require.Equal(t, "sorry, too many clients already", tooMany.reason)

	// Everything else that keeps the check from logging in proves nothing
	// about capacity: unknown, never available.
	for name, reading := range map[string]connectionReading{
		"no primary": classifyConnections("", fmt.Errorf("%w: sel in ns", kube.ErrNoReadyPod), nil),
		"timeout":    classifyConnections("", errors.New("stream closed"), context.DeadlineExceeded),
		"no role": classifyConnections("", errors.New(`kube: exec in ns/pod: exit code 2 (stderr: psql: error: `+
			`FATAL:  role "skali_probe" does not exist`+"\n)"), nil),
		"garbage": classifyConnections("not json", nil, nil),
	} {
		require.Equal(t, module.ConnectionsUnknown, reading.state, name)
		require.NotEmpty(t, reading.reason, name)
		require.Nil(t, reading.usage, name)
	}
	require.Equal(t, "the connection check's role is not set up on the pool yet",
		classifyConnections("", errors.New(`FATAL:  role "skali_probe" does not exist`), nil).reason)
	require.Equal(t, "the connection check failed: psql: error: connection refused",
		classifyConnections("", errors.New("kube: exec in ns/pod: exit code 2 (stderr: psql: error: connection refused\n)"), nil).reason)
}

// A verdict changes only when the same answer comes back twice in a row;
// the first answer about a pool is taken at once.
func TestConnectionVerdictNeedsConfirmation(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	at := func(i int) time.Time { return start.Add(time.Duration(i) * ConnectionCheckInterval) }
	ok := func(used int32) connectionReading {
		return connectionReading{state: module.ConnectionsAvailable,
			usage: &cnpg.ProbeUsage{Max: 100, Reserved: 3, Used: used}}
	}
	refused := connectionReading{state: module.ConnectionsRefusing, reason: "sorry, too many clients already"}
	unknown := connectionReading{state: module.ConnectionsUnknown, reason: "no ready primary instance to check"}

	var pool poolConnections
	require.True(t, pool.observe(ok(10), at(0)))
	require.Equal(t, module.ConnectionsAvailable, pool.current.State)
	require.Equal(t, at(0), pool.current.Since)

	// Usage follows every successful check without a verdict change.
	require.False(t, pool.observe(ok(20), at(1)))
	require.Equal(t, int32(20), pool.current.Used)
	require.Equal(t, int32(97), pool.current.Limit)
	require.Equal(t, at(1), pool.current.CheckedAt)

	// One refusal is not yet a verdict; one success in between resets it.
	require.False(t, pool.observe(refused, at(2)))
	require.False(t, pool.observe(ok(20), at(3)))
	require.False(t, pool.observe(refused, at(4)))
	require.Equal(t, module.ConnectionsAvailable, pool.current.State)
	// A different answer restarts the count rather than adding to it.
	require.False(t, pool.observe(unknown, at(5)))
	require.False(t, pool.observe(refused, at(6)))
	require.True(t, pool.observe(refused, at(7)))
	require.Equal(t, module.ConnectionsRefusing, pool.current.State)
	require.Equal(t, "sorry, too many clients already", pool.current.Reason)
	require.Equal(t, at(7), pool.current.Since)
	// The failed checks kept the last usage with its time.
	require.Equal(t, at(3), pool.current.CheckedAt)

	require.False(t, pool.observe(ok(5), at(8)))
	require.Equal(t, module.ConnectionsRefusing, pool.current.State, "one login is no recovery")
	require.True(t, pool.observe(ok(5), at(9)))
	require.Equal(t, module.ConnectionsAvailable, pool.current.State)
	require.Empty(t, pool.current.Reason)
}

// scriptedPools answers each pool's connection check from a per-pool
// script; the Cluster methods the monitor never calls stay nil.
type scriptedPools struct {
	Cluster
	mu      sync.Mutex
	answers map[string]func() (string, error)
	checks  map[string]int
}

func (s *scriptedPools) set(pool string, answer func() (string, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers[pool] = answer
}

func (s *scriptedPools) ExecInPod(_ context.Context, _, selector, container string, command []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pool := strings.TrimPrefix(strings.Split(selector, ",")[0], cnpg.LabelCluster+"=")
	if selector != cnpg.PrimarySelector(pool) || container != cnpg.PostgresContainer ||
		!strings.Contains(strings.Join(command, " "), "-U "+cnpg.ProbeRole) {
		return "", fmt.Errorf("unexpected exec %s %s %v", selector, container, command)
	}
	s.checks[pool]++
	answer, ok := s.answers[pool]
	if !ok {
		return "", fmt.Errorf("%w: %s", kube.ErrNoReadyPod, selector)
	}
	return answer()
}

func connectionsOf(t *testing.T, objects []observe.Object) map[string]module.DatabaseConnectionsStatus {
	t.Helper()
	out := map[string]module.DatabaseConnectionsStatus{}
	for _, obj := range objects {
		require.Equal(t, module.KindDatabaseConnections, obj.Kind)
		require.Equal(t, obj.Name, obj.SharedKey, "the check joins its pool's environments by the pool name")
		out[obj.Name] = *obj.DatabaseConnections
	}
	return out
}

// The monitor opens an incident on a confirmed refusal and closes it only
// on confirmed logins, and a restarted monitor neither forgets an open
// incident nor recovers it on a single login.
func TestConnectionMonitorRecordsIncidents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := dbstore.New(store.NewStore(testdb.New(t)))
	shared, err := db.CreateCluster(ctx, dbstore.ClusterInput{
		Name: "pg17-shared", Engine: "postgres", Major: 17, Class: dbstore.ClassShared,
		Instances: 1, StorageBytes: 1 << 30, Image: "image",
	})
	require.NoError(t, err)
	releasing, err := db.CreateCluster(ctx, dbstore.ClusterInput{
		Name: "pg18-shared", Engine: "postgres", Major: 18, Class: dbstore.ClassShared,
		Instances: 1, StorageBytes: 1 << 30, Image: "image",
	})
	require.NoError(t, err)
	_, err = db.TransitionCluster(ctx, releasing.ID, dbstore.StateReleasing)
	require.NoError(t, err)

	pools := &scriptedPools{answers: map[string]func() (string, error){}, checks: map[string]int{}}
	controller := New(Deps{DB: db, Cluster: pools, Observed: observe.NewStore(nil)}, Config{})
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	monitor := newConnectionMonitor(controller, func() time.Time { return clock })
	round := func(m *connectionMonitor) module.DatabaseConnectionsStatus {
		t.Helper()
		clock = clock.Add(ConnectionCheckInterval)
		objects, err := m.round(ctx)
		require.NoError(t, err)
		statuses := connectionsOf(t, objects)
		require.Len(t, statuses, 1, "a releasing pool is not checked")
		return statuses[shared.Name]
	}
	incidents := func() []store.PoolConnectionIncident {
		t.Helper()
		rows, err := db.ConnectionIncidents(ctx, shared.ID, 10)
		require.NoError(t, err)
		return rows
	}
	accept := func() (string, error) { return usageJSON(12, 100), nil }
	refuse := func() (string, error) { return "", refusedExec }

	pools.set(shared.Name, accept)
	status := round(monitor)
	require.Equal(t, module.ConnectionsAvailable, status.State)
	require.Equal(t, int32(12), status.Used)
	require.Equal(t, map[string]int32{"app": 12}, status.Databases)
	require.Zero(t, pools.checks[releasing.Name])

	pools.set(shared.Name, refuse)
	require.Equal(t, module.ConnectionsAvailable, round(monitor).State)
	require.Empty(t, incidents(), "one refusal opens nothing")
	status = round(monitor)
	require.Equal(t, module.ConnectionsRefusing, status.State)
	opened := incidents()
	require.Len(t, opened, 1)
	require.Nil(t, opened[0].ClosedAt)
	require.Equal(t, ConnectionIncidentCode, opened[0].Code)
	require.Equal(t, "remaining connection slots are reserved for roles with the SUPERUSER attribute", opened[0].Message)
	require.WithinDuration(t, status.Since, opened[0].OpenedAt, 0)

	// A check that cannot run proves no recovery: the incident stays open.
	pools.set(shared.Name, func() (string, error) { return "", fmt.Errorf("%w: gone", kube.ErrNoReadyPod) })
	round(monitor)
	require.Equal(t, module.ConnectionsUnknown, round(monitor).State)
	require.Nil(t, incidents()[0].ClosedAt)

	// A restarted daemon starts from the open incident: one login is not
	// enough to close it, and the incident keeps its identity.
	restarted := newConnectionMonitor(controller, func() time.Time { return clock })
	pools.set(shared.Name, accept)
	status = round(restarted)
	require.Equal(t, module.ConnectionsRefusing, status.State)
	require.WithinDuration(t, opened[0].OpenedAt, status.Since, 0)
	require.Nil(t, incidents()[0].ClosedAt)
	require.Equal(t, module.ConnectionsAvailable, round(restarted).State)
	closed := incidents()
	require.Len(t, closed, 1)
	require.Equal(t, opened[0].ID, closed[0].ID)
	require.NotNil(t, closed[0].ClosedAt)
	require.WithinDuration(t, clock, *closed[0].ClosedAt, 0)

	// The next refusal is a new incident.
	pools.set(shared.Name, refuse)
	round(restarted)
	round(restarted)
	require.Len(t, incidents(), 2)
	require.Nil(t, incidents()[0].ClosedAt)
}
