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
