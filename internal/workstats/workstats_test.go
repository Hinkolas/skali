package workstats

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/util/workqueue"
)

func newTestQueue() *Queue[string] {
	return NewQueue(workqueue.DefaultTypedControllerRateLimiter[string](), func(string) string { return "item" })
}

// An event that arrives while the key's pass runs waits for the rest of
// that pass: the handoff delay the queue accounting exists to expose.
func TestQueueChargesTheRestOfARunningPass(t *testing.T) {
	q := newTestQueue()
	defer q.ShutDown()

	q.Add("a", "first")
	key, waited, shutdown := q.Take()
	require.False(t, shutdown)
	require.Equal(t, "a", key)
	require.Less(t, waited, 50*time.Millisecond)

	q.Add("a", "second")
	time.Sleep(40 * time.Millisecond)
	q.Done("a") // the workqueue hands the dirty key out again

	_, waited, _ = q.Take()
	require.GreaterOrEqual(t, waited, 40*time.Millisecond)
	q.Done("a")

	stats := q.Stats()
	require.Equal(t, 0, stats.Depth)
	require.Equal(t, 0, stats.Active)
	require.Len(t, stats.Kinds, 1)
	kind := stats.Kinds[0]
	require.Equal(t, "item", kind.Kind)
	require.Equal(t, map[string]uint64{"first": 1, "second": 1}, kind.Arrivals)
	require.EqualValues(t, 2, kind.Wait.Count)
	require.EqualValues(t, 2, kind.Pass.Count)
	require.GreaterOrEqual(t, kind.Pass.Max, 40*time.Millisecond)
}

// A delayed add waits from the end of its delay, not from the call.
func TestQueueWaitStartsWhenTheDelayEnds(t *testing.T) {
	q := newTestQueue()
	defer q.ShutDown()

	q.AddAfter("b", 60*time.Millisecond, "requeue")
	started := time.Now()
	key, waited, _ := q.Take()
	require.Equal(t, "b", key)
	require.GreaterOrEqual(t, time.Since(started), 50*time.Millisecond)
	require.Less(t, waited, 50*time.Millisecond)
	q.Done("b")

	// The rate-limited path is the same delay with the limiter's backoff.
	q.AddRateLimited("b", "retry")
	_, _, _ = q.Take()
	q.Done("b")
	require.Equal(t, map[string]uint64{"requeue": 1, "retry": 1}, q.Stats().Kinds[0].Arrivals)
}

// A sweep's phases are stable per key, inside the interval, and spread.
func TestPhaseIsStableAndSpread(t *testing.T) {
	interval := 10 * time.Minute
	id := uuid.New()
	require.Equal(t, Phase(id, interval), Phase(id, interval))
	require.Zero(t, Phase(id, 0))

	var halves [2]int
	for range 1000 {
		phase := Phase(uuid.New(), interval)
		require.True(t, phase >= 0 && phase < interval)
		halves[phase*2/interval]++
	}
	require.InDelta(t, 500, halves[0], 100, "phases spread across the interval")
}

func TestHistogramIsCumulative(t *testing.T) {
	var h Histogram
	h.observe(300 * time.Millisecond)
	h.observe(3 * time.Second)
	h.observe(10 * time.Minute)
	require.EqualValues(t, 3, h.Count)
	require.Equal(t, 10*time.Minute, h.Max)
	require.EqualValues(t, 0, h.Buckets[1], "≤ 250ms")
	require.EqualValues(t, 1, h.Buckets[2], "≤ 500ms")
	require.EqualValues(t, 2, h.Buckets[5], "≤ 5s")
	require.EqualValues(t, 2, h.Buckets[len(Bounds)-1], "above the last bound only in Count")
}

func TestPassChargesRequestsStatementsAndLock(t *testing.T) {
	const caller = "test-pass-caller"
	p := NewPass(caller)
	ctx := WithPass(context.Background(), p)
	pods := url.URL{Path: "/api/v1/namespaces/{namespace}/pods/{name}"}

	PassFrom(ctx).Mark("load")
	requestHook{}.Observe(ctx, "GET", pods, 2*time.Second)
	throttleHook{}.Observe(ctx, "GET", pods, time.Second)
	queryCtx := DBTracer{}.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{})
	DBTracer{}.TraceQueryEnd(queryCtx, nil, pgx.TraceQueryEndData{})
	p.AddLock(10*time.Millisecond, 20*time.Millisecond)

	_, attrs := p.end()
	logged := map[string]any{}
	for i := 0; i+1 < len(attrs); i += 2 {
		logged[attrs[i].(string)] = attrs[i+1]
	}
	require.Contains(t, logged["phases"], "load=")
	require.Contains(t, logged["phases"], "rest=")
	require.Equal(t, 1, logged["db_queries"])
	require.Equal(t, 1, logged["kube_requests"])
	require.Equal(t, time.Second, logged["throttled"])
	require.Equal(t, 20*time.Millisecond, logged["lock_wait"])

	var totals *CallerTotals
	for _, candidate := range Totals() {
		if candidate.Caller == caller {
			totals = &candidate
		}
	}
	require.NotNil(t, totals)
	require.EqualValues(t, 1, totals.Passes)
	require.Equal(t, 2*time.Second, totals.Kube)

	var request *RequestTotals
	for _, candidate := range Requests() {
		if candidate.Caller == caller {
			request = &candidate
		}
	}
	require.Equal(t, &RequestTotals{Caller: caller, Verb: "GET", Resource: "pods", Count: 1,
		Took: 2 * time.Second, Throttled: time.Second}, request)
}

// Work outside a pass is never charged and its requests are unattributed.
func TestNoPassRecordsNothing(t *testing.T) {
	var p *Pass
	p.Mark("ignored")
	p.AddLock(time.Second, time.Second)

	ctx := context.Background()
	require.Equal(t, ctx, DBTracer{}.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{}))
	require.Equal(t, Unattributed, callerOf(ctx))
}

func TestResource(t *testing.T) {
	for path, want := range map[string]string{
		"/api/v1/namespaces/{namespace}/pods/{name}":                                   "pods",
		"/api/v1/namespaces/{namespace}/pods/{name}/exec":                              "pods/exec",
		"/apis/apps/v1/namespaces/{namespace}/deployments":                             "deployments",
		"/apis/apps/v1/namespaces/{namespace}/deployments/{name}/scale":                "deployments/scale",
		"/apis/postgresql.cnpg.io/v1/namespaces/{namespace}/clusters":                  "clusters",
		"/api/v1/namespaces/{name}":                                                    "namespaces",
		"/api/v1/nodes":                                                                "nodes",
		"/prefix/apis/traefik.io/v1alpha1/namespaces/{namespace}/ingressroutes/{name}": "ingressroutes",
		"/apis":    "discovery",
		"/version": "/version",
	} {
		require.Equal(t, want, resource(path), path)
	}
}
