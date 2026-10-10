package workstats

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/tools/metrics"
)

// RegisterClientHooks routes client-go's request metrics into the
// accounting: every request's latency, and its wait for the client's
// request budget, which client-go itself only logs once a single wait
// passes a second. client-go accepts one registration per process; skalid
// makes it at startup, before building its clients.
func RegisterClientHooks() {
	metrics.Register(metrics.RegisterOpts{
		RequestLatency:     requestHook{},
		RateLimiterLatency: throttleHook{},
	})
}

// RequestTotals sums the Kubernetes requests of one caller, verb, and
// resource since the process started. Took includes Throttled.
type RequestTotals struct {
	Caller    string
	Verb      string
	Resource  string
	Count     uint64
	Took      time.Duration
	Throttled time.Duration
}

type requestKey struct{ caller, verb, resource string }

var requests = struct {
	mu sync.Mutex
	by map[requestKey]*RequestTotals
}{by: map[requestKey]*RequestTotals{}}

func recordRequest(ctx context.Context, verb string, u url.URL, f func(*RequestTotals)) {
	key := requestKey{caller: callerOf(ctx), verb: verb, resource: resource(u.Path)}
	requests.mu.Lock()
	defer requests.mu.Unlock()
	t, ok := requests.by[key]
	if !ok {
		t = &RequestTotals{Caller: key.caller, Verb: key.verb, Resource: key.resource}
		requests.by[key] = t
	}
	f(t)
}

// Requests reads the request totals, ordered by caller, resource, and verb.
func Requests() []RequestTotals {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	out := make([]RequestTotals, 0, len(requests.by))
	for _, t := range requests.by {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b RequestTotals) int {
		return cmp.Or(strings.Compare(a.Caller, b.Caller), strings.Compare(a.Resource, b.Resource),
			strings.Compare(a.Verb, b.Verb))
	})
	return out
}

type requestHook struct{}

func (requestHook) Observe(ctx context.Context, verb string, u url.URL, latency time.Duration) {
	PassFrom(ctx).charge(func(c *Cost) {
		c.KubeRequests++
		c.Kube += latency
	})
	recordRequest(ctx, verb, u, func(t *RequestTotals) {
		t.Count++
		t.Took += latency
	})
}

type throttleHook struct{}

func (throttleHook) Observe(ctx context.Context, verb string, u url.URL, latency time.Duration) {
	PassFrom(ctx).charge(func(c *Cost) { c.Throttled += latency })
	recordRequest(ctx, verb, u, func(t *RequestTotals) { t.Throttled += latency })
}

// resource names the API resource of a request path as client-go templates
// it, /apis/apps/v1/namespaces/{namespace}/deployments/{name}/scale: the
// resource, followed by the subresource when there is one
// ("deployments/scale"). Paths outside a group version (discovery,
// /version) name themselves.
func resource(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	start := slices.IndexFunc(segments, func(s string) bool { return s == "api" || s == "apis" })
	if start < 0 {
		return path
	}
	skip := 2 // api/v1
	if segments[start] == "apis" {
		skip = 3 // apis/group/version
	}
	segments = segments[min(start+skip, len(segments)):]
	if len(segments) >= 3 && segments[0] == "namespaces" {
		segments = segments[2:]
	}
	switch len(segments) {
	case 0:
		return "discovery"
	case 1, 2:
		return segments[0]
	default:
		return segments[0] + "/" + segments[2]
	}
}
