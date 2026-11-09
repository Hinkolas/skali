package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

// A trimmed capture of a real k3d Traefik 3.3 exposition with
// addRoutersLabels on: router names carry the CRD provider's rule-hash
// suffix, and dimensions (code/method) split one router across series.
const traefikFixture = `# HELP traefik_router_requests_total How many HTTP requests are processed on a router, partitioned by service, status code, protocol, and method.
# TYPE traefik_router_requests_total counter
traefik_router_requests_total{code="200",method="GET",protocol="http",router="skali-whoami-local-whoami-whoami-public-8fe1804f28f11a756ed7@kubernetescrd",service="x@kubernetescrd"} 30
traefik_router_requests_total{code="404",method="GET",protocol="http",router="skali-whoami-local-whoami-whoami-public-8fe1804f28f11a756ed7@kubernetescrd",service="x@kubernetescrd"} 2
traefik_router_requests_total{code="301",method="GET",protocol="http",router="skali-shop-prod-shop-web-public-http-0011223344556677aabb@kubernetescrd",service="x@kubernetescrd"} 5
# HELP traefik_router_requests_bytes_total The total size of requests in bytes handled by a router, partitioned by status code, protocol, and method.
# TYPE traefik_router_requests_bytes_total counter
traefik_router_requests_bytes_total{code="200",method="GET",protocol="http",router="skali-whoami-local-whoami-whoami-public-8fe1804f28f11a756ed7@kubernetescrd",service="x@kubernetescrd"} 128
# HELP traefik_router_responses_bytes_total The total size of responses in bytes handled by a router, partitioned by status code, protocol, and method.
# TYPE traefik_router_responses_bytes_total counter
traefik_router_responses_bytes_total{code="200",method="GET",protocol="http",router="skali-whoami-local-whoami-whoami-public-8fe1804f28f11a756ed7@kubernetescrd",service="x@kubernetescrd"} 12870
traefik_router_responses_bytes_total{code="200",method="GET",protocol="http",router="dashboard@internal",service="dashboard@internal"} 999
`

func TestParseRouterCounters(t *testing.T) {
	perRouter, err := parseRouterCounters([]byte(traefikFixture))
	require.NoError(t, err)

	whoami := perRouter["skali-whoami-local-whoami-whoami-public-8fe1804f28f11a756ed7@kubernetescrd"]
	require.EqualValues(t, 32, whoami.requests, "code dimensions must sum")
	require.EqualValues(t, 128, whoami.requestBytes)
	require.EqualValues(t, 12870, whoami.responseBytes)

	redirect := perRouter["skali-shop-prod-shop-web-public-http-0011223344556677aabb@kubernetescrd"]
	require.EqualValues(t, 5, redirect.requests)

	// Internal routers parse too; the route index simply never matches them.
	require.Contains(t, perRouter, "dashboard@internal")
}

func TestRouterCountersDelta(t *testing.T) {
	previous := routerCounters{requests: 10, requestBytes: 100, responseBytes: 1000}
	current := routerCounters{requests: 14, requestBytes: 150, responseBytes: 1400}
	require.Equal(t, routerCounters{requests: 4, requestBytes: 50, responseBytes: 400}, current.delta(previous))

	// A reset (any component shrinking) books the post-restart totals.
	reset := routerCounters{requests: 3, requestBytes: 30, responseBytes: 300}
	require.Equal(t, reset, reset.delta(previous))
}

// One Traefik pod runs per edge node and counts only its own requests: the
// stored delta is the sum over pods, each differenced against its own
// previous scrape.
func TestEdgeDeltasSumPerPod(t *testing.T) {
	const router = "skali-shop-prod-shop-web-public-0011@kubernetescrd"
	counters := func(requests int64) map[string]routerCounters {
		return map[string]routerCounters{router: {requests: requests, requestBytes: requests * 10, responseBytes: requests * 100}}
	}
	listed := func(pods ...string) map[string]struct{} {
		set := make(map[string]struct{}, len(pods))
		for _, pod := range pods {
			set[pod] = struct{}{}
		}
		return set
	}
	var last map[string]map[string]routerCounters
	round := func(scrapes map[string]map[string]routerCounters, running map[string]struct{}) map[string]routerCounters {
		before := len(last)
		next, deltas := edgeDeltas(last, scrapes, running)
		require.Len(t, last, before, "the previous baselines stay untouched until the caller commits")
		last = next
		return deltas
	}

	// First sight of both pods only records baselines.
	deltas := round(map[string]map[string]routerCounters{"a": counters(100), "b": counters(40)}, listed("a", "b"))
	require.Empty(t, deltas)

	// Both advance: the deltas add up across pods.
	deltas = round(map[string]map[string]routerCounters{"a": counters(110), "b": counters(45)}, listed("a", "b"))
	require.Equal(t, routerCounters{requests: 15, requestBytes: 150, responseBytes: 1500}, deltas[router])

	// An uncommitted round (the insert failed) is computed again from the
	// same baselines next time, so its traffic is not lost.
	uncommitted, _ := edgeDeltas(last, map[string]map[string]routerCounters{"a": counters(130)}, listed("a", "b"))
	require.EqualValues(t, 130, uncommitted["a"][router].requests)
	deltas = round(map[string]map[string]routerCounters{"a": counters(131), "b": counters(45)}, listed("a", "b"))
	require.EqualValues(t, 21, deltas[router].requests)

	// Pod b's scrape fails this round but it is still running: its baseline
	// survives, and its next answer covers both intervals.
	deltas = round(map[string]map[string]routerCounters{"a": counters(133)}, listed("a", "b"))
	require.EqualValues(t, 2, deltas[router].requests)
	deltas = round(map[string]map[string]routerCounters{"a": counters(133), "b": counters(52)}, listed("a", "b"))
	require.EqualValues(t, 7, deltas[router].requests)

	// Pod a restarts its container (counters reset); a new pod c appears and
	// only baselines; pod b is gone and drops its baseline.
	deltas = round(map[string]map[string]routerCounters{"a": counters(3), "c": counters(500)}, listed("a", "c"))
	require.EqualValues(t, 3, deltas[router].requests, "a reset books the post-restart total, the new pod nothing")
	require.NotContains(t, last, "b")
	require.Contains(t, last, "c")
}

func TestMatchRouter(t *testing.T) {
	envA, envB := uuid.New(), uuid.New()
	index := map[string]routeIdentity{
		"skali-whoami-local-whoami-whoami-public": {environmentID: envA, applicationKey: "whoami", routeKey: "public"},
		// A second entry sharing a prefix: longest match must win.
		"skali-whoami-local-whoami-whoami-public-admin": {environmentID: envB, applicationKey: "whoami", routeKey: "public-admin"},
	}

	identity, ok := matchRouter("skali-whoami-local-whoami-whoami-public-8fe1804f28f11a756ed7@kubernetescrd", index)
	require.True(t, ok)
	require.Equal(t, envA, identity.environmentID)

	identity, ok = matchRouter("skali-whoami-local-whoami-whoami-public-admin-aabbccddeeff00112233@kubernetescrd", index)
	require.True(t, ok)
	require.Equal(t, "public-admin", identity.routeKey)

	// Exact name without a hash segment (older providers) matches too.
	identity, ok = matchRouter("skali-whoami-local-whoami-whoami-public@kubernetes", index)
	require.True(t, ok)
	require.Equal(t, "public", identity.routeKey)

	_, ok = matchRouter("dashboard@internal", index)
	require.False(t, ok)
}

func TestEnvironmentSeriesEdge(t *testing.T) {
	ctx := context.Background()
	st := store.NewStore(testdb.New(t))
	envID := seedEnvironment(t, st)
	svc := &Service{Store: st}

	now := time.Date(2026, 8, 20, 12, 0, 30, 0, time.UTC)
	_, err := st.InsertEdgeMetricSamples(ctx, store.InsertEdgeMetricSamplesParams{
		SampledAt:       now,
		EnvironmentIds:  []uuid.UUID{envID, envID},
		ApplicationKeys: []string{"web", "web"},
		RouteKeys:       []string{"public", "internal"},
		Requests:        []int64{10, 4},
		RequestBytes:    []int64{100, 40},
		ResponseBytes:   []int64{1000, 400},
	})
	require.NoError(t, err)

	w, err := WindowByName("1h")
	require.NoError(t, err)
	series, err := svc.EnvironmentSeries(ctx, envID, w, now)
	require.NoError(t, err)
	require.Len(t, series.Apps, 1)

	app := series.Apps[0]
	require.Equal(t, "web", app.Key)
	require.NotNil(t, app.Edge)
	// Routes sum per application and bucket.
	last := app.Edge.Requests[59]
	require.NotNil(t, last)
	require.EqualValues(t, 14, *last)
	require.Nil(t, app.Edge.Requests[0])
	// No usage samples were written: cpu stays all-nil alongside edge data.
	require.Nil(t, app.CPUMillicores[59])
}
