package metrics

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/edge"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/platform"
	"github.com/Hinkolas/skali/internal/store"
)

// Traefik's k3s chart serves Prometheus metrics on the dedicated metrics
// entrypoint; the bundle's HelmChartConfig overlay exposes it and turns on
// per-router labels. The edge runs one Traefik pod per edge node, each
// counting only the requests it served, so the sampler reads every pod
// instead of the Service (which would answer from a random one).
const (
	traefikNamespace   = platform.EdgeNamespace
	traefikMetricsPort = 9100
)

// edgeScrapeTimeout bounds one pod's metrics scrape, well inside the
// sampler's tick timeout; a variable so tests can shorten it.
var edgeScrapeTimeout = 5 * time.Second

// The router counter families the sampler consumes. Names verified against
// Traefik 3.3 (chart 34.x); all carry the "router" label when
// addRoutersLabels is on.
const (
	familyRequests      = "traefik_router_requests_total"
	familyRequestBytes  = "traefik_router_requests_bytes_total"
	familyResponseBytes = "traefik_router_responses_bytes_total"
)

// routerCounters is one router's cumulative totals at scrape time.
type routerCounters struct {
	requests      int64
	requestBytes  int64
	responseBytes int64
}

func (c routerCounters) delta(prev routerCounters) routerCounters {
	d := routerCounters{
		requests:      c.requests - prev.requests,
		requestBytes:  c.requestBytes - prev.requestBytes,
		responseBytes: c.responseBytes - prev.responseBytes,
	}
	if d.requests < 0 || d.requestBytes < 0 || d.responseBytes < 0 {
		// Traefik restarted and its counters reset; the current totals are
		// the traffic since then.
		return c
	}
	return d
}

// routeIdentity places one IngressRoute in the product model.
type routeIdentity struct {
	environmentID  uuid.UUID
	applicationKey string
	routeKey       string
}

// sampleEdge differences each Traefik pod's per-router counters against
// that pod's previous scrape and stores the summed deltas per
// (environment, application, route). The first sight of a pod or of a
// router on it only records its baseline, so a skalid restart or a new
// edge node loses one interval instead of booking lifetime totals as a
// spike.
func (s *Sampler) sampleEdge(ctx context.Context, now time.Time) error {
	pods, err := s.Kube.Clientset.CoreV1().Pods(traefikNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: platform.EdgePodLabel + "=" + platform.EdgePodName,
	})
	if err != nil {
		return fmt.Errorf("list traefik pods: %w", err)
	}
	type scrape struct {
		pod      string
		counters map[string]routerCounters
		err      error
	}
	listed := make(map[string]struct{}, len(pods.Items))
	results := make(chan scrape, len(pods.Items))
	running := 0
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		id := string(pod.UID)
		listed[id] = struct{}{}
		running++
		// Each pod gets its own bounded scrape, concurrently, so one stalled
		// edge node neither delays the others nor spends the tick's deadline
		// that aggregation, storage, and the later collectors still need.
		go func(id, name string) {
			scrapeCtx, cancel := context.WithTimeout(ctx, edgeScrapeTimeout)
			defer cancel()
			body, status, err := s.Kube.PodProxyDo(scrapeCtx, http.MethodGet, traefikNamespace, name,
				traefikMetricsPort, "/metrics", nil, nil)
			switch {
			case err != nil:
				results <- scrape{pod: id, err: err}
			case status != http.StatusOK:
				results <- scrape{pod: id}
			default:
				perRouter, err := parseRouterCounters(body)
				if err != nil {
					err = fmt.Errorf("parse traefik metrics from %s: %w", name, err)
				}
				results <- scrape{pod: id, counters: perRouter, err: err}
			}
		}(id, pod.Name)
	}
	scrapes := make(map[string]map[string]routerCounters, running)
	var lastErr error
	for range running {
		result := <-results
		switch {
		case result.err != nil:
			// The pod keeps its baseline: its next answer covers both
			// intervals instead of losing one.
			lastErr = result.err
		case result.counters != nil:
			scrapes[result.pod] = result.counters
		}
	}
	if len(scrapes) == 0 {
		if lastErr != nil {
			return lastErr
		}
		// Expected until the chart overlay has rolled Traefik (fresh install
		// mid-converge, or an upgrade whose helm re-render is pending); say
		// so once instead of warning every tick.
		if !s.edgeUnavailable {
			s.edgeUnavailable = true
			slog.InfoContext(ctx, "edge metrics endpoint unavailable; waiting for the traefik overlay")
		}
		return nil
	}
	s.edgeUnavailable = false

	// The new baselines replace the old ones only once their deltas are
	// stored (or there is nothing to store): a failed route listing or
	// insert retries the same interval on the next tick instead of
	// dropping it.
	baselines, deltas := edgeDeltas(s.lastEdge, scrapes, listed)
	if len(deltas) == 0 {
		s.lastEdge = baselines
		return lastErr
	}

	index, err := s.routeIndex(ctx)
	if err != nil {
		return err
	}

	type key struct {
		env   uuid.UUID
		app   string
		route string
	}
	agg := make(map[key]*routerCounters)
	var order []key
	for router, d := range deltas {
		identity, ok := matchRouter(router, index)
		if !ok {
			continue
		}
		k := key{env: identity.environmentID, app: identity.applicationKey, route: identity.routeKey}
		row := agg[k]
		if row == nil {
			row = &routerCounters{}
			agg[k] = row
			order = append(order, k)
		}
		row.requests += d.requests
		row.requestBytes += d.requestBytes
		row.responseBytes += d.responseBytes
	}

	if len(order) == 0 {
		s.lastEdge = baselines
		return lastErr
	}
	params := store.InsertEdgeMetricSamplesParams{SampledAt: now}
	for _, k := range order {
		row := agg[k]
		params.EnvironmentIds = append(params.EnvironmentIds, k.env)
		params.ApplicationKeys = append(params.ApplicationKeys, k.app)
		params.RouteKeys = append(params.RouteKeys, k.route)
		params.Requests = append(params.Requests, row.requests)
		params.RequestBytes = append(params.RequestBytes, row.requestBytes)
		params.ResponseBytes = append(params.ResponseBytes, row.responseBytes)
	}
	if _, err := s.Store.InsertEdgeMetricSamples(ctx, params); err != nil {
		return fmt.Errorf("insert edge samples: %w", err)
	}
	s.lastEdge = baselines
	return lastErr
}

// edgeDeltas folds one round of per-pod scrapes into per-router deltas
// summed across pods, and returns the baselines the next round compares
// against, leaving last untouched. A pod or a router seen for the first
// time only sets its baseline; routers gone from a pod's scrape lose
// theirs, so a returning namesake starts fresh. Pods no longer running
// (listed holds the running ones) drop their baselines; a running pod
// whose scrape failed keeps its own.
func edgeDeltas(last map[string]map[string]routerCounters,
	scrapes map[string]map[string]routerCounters, listed map[string]struct{},
) (map[string]map[string]routerCounters, map[string]routerCounters) {
	next := make(map[string]map[string]routerCounters, len(listed))
	for pod, counters := range last {
		if _, ok := listed[pod]; ok {
			next[pod] = counters
		}
	}
	deltas := make(map[string]routerCounters)
	for pod, current := range scrapes {
		previous, known := last[pod]
		next[pod] = current
		if !known {
			continue
		}
		for router, counters := range current {
			before, ok := previous[router]
			if !ok {
				continue
			}
			d := counters.delta(before)
			sum := deltas[router]
			sum.requests += d.requests
			sum.requestBytes += d.requestBytes
			sum.responseBytes += d.responseBytes
			deltas[router] = sum
		}
	}
	return next, deltas
}

// parseRouterCounters reduces the Prometheus text exposition to per-router
// totals, summing away the code/method/protocol dimensions.
func parseRouterCounters(body []byte) (map[string]routerCounters, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	perRouter := make(map[string]routerCounters)
	for _, family := range []string{familyRequests, familyRequestBytes, familyResponseBytes} {
		mf, ok := families[family]
		if !ok {
			continue
		}
		for _, metric := range mf.GetMetric() {
			router := ""
			for _, label := range metric.GetLabel() {
				if label.GetName() == "router" {
					router = label.GetValue()
					break
				}
			}
			if router == "" {
				continue
			}
			value := int64(math.Round(metric.GetCounter().GetValue()))
			counters := perRouter[router]
			switch family {
			case familyRequests:
				counters.requests += value
			case familyRequestBytes:
				counters.requestBytes += value
			case familyResponseBytes:
				counters.responseBytes += value
			}
			perRouter[router] = counters
		}
	}
	return perRouter, nil
}

// routeIndex maps "<namespace>-<name>" of every managed IngressRoute to its
// product identity. The route key is the object name minus the rendered
// "<project>-<app>-" prefix (labels supply both parts), with the plain-HTTP
// sibling's "-http" suffix merged into its TLS twin's key. Names past the
// 63-char limit carry a rendered hash suffix; their derived key keeps it,
// which is stable per route and good enough for a breakdown column.
func (s *Sampler) routeIndex(ctx context.Context) (map[string]routeIdentity, error) {
	list, err := s.Kube.Dynamic.Resource(edge.IngressRouteGVR).Namespace(metav1.NamespaceAll).
		List(ctx, metav1.ListOptions{LabelSelector: rendering.ManagedSelector})
	if err != nil {
		return nil, fmt.Errorf("list ingress routes: %w", err)
	}
	index := make(map[string]routeIdentity, len(list.Items))
	for _, item := range list.Items {
		labels := item.GetLabels()
		environmentID, err := uuid.Parse(labels[rendering.LabelEnvironment])
		if err != nil {
			continue
		}
		app := labels[rendering.LabelApplication]
		if app == "" {
			continue
		}
		name := item.GetName()
		routeKey := name
		prefix := strings.ToLower(labels[rendering.LabelProject] + "-" + app + "-")
		if strings.HasPrefix(name, prefix) {
			routeKey = name[len(prefix):]
		}
		routeKey = strings.TrimSuffix(routeKey, "-http")
		index[item.GetNamespace()+"-"+name] = routeIdentity{
			environmentID:  environmentID,
			applicationKey: app,
			routeKey:       routeKey,
		}
	}
	return index, nil
}

// matchRouter resolves a Traefik router label ("<ns>-<name>-<rulehash>@<provider>",
// hash and provider optional across versions) to an indexed IngressRoute by
// longest prefix: hyphens inside both namespace and name rule out splitting,
// so the known object names anchor the match instead.
func matchRouter(router string, index map[string]routeIdentity) (routeIdentity, bool) {
	base := router
	if i := strings.IndexByte(base, '@'); i >= 0 {
		base = base[:i]
	}
	if identity, ok := index[base]; ok {
		return identity, true
	}
	bestLen := -1
	var best routeIdentity
	for name, identity := range index {
		if len(name) > bestLen && strings.HasPrefix(base, name+"-") {
			bestLen = len(name)
			best = identity
		}
	}
	return best, bestLen >= 0
}
