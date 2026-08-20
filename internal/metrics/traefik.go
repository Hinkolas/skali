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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/edge"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/store"
)

// Traefik's k3s chart serves Prometheus metrics on the dedicated metrics
// entrypoint; the bundle's HelmChartConfig overlay exposes it on the Service
// and turns on per-router labels.
const (
	traefikNamespace   = "kube-system"
	traefikService     = "traefik"
	traefikMetricsPort = 9100
)

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

// sampleEdge differences Traefik's per-router counters against the previous
// scrape and stores the deltas per (environment, application, route). The
// first sight of a router only records its baseline, so a skalid restart
// loses one interval instead of booking the lifetime totals as a spike.
func (s *Sampler) sampleEdge(ctx context.Context, now time.Time) error {
	body, status, err := s.Kube.ServiceProxyDo(ctx, http.MethodGet, traefikNamespace, traefikService,
		traefikMetricsPort, "/metrics", nil, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		// Expected until the chart overlay has rolled Traefik (fresh install
		// mid-converge, or an upgrade whose helm re-render is pending); say
		// so once instead of warning every tick.
		if !s.edgeUnavailable {
			s.edgeUnavailable = true
			slog.InfoContext(ctx, "edge metrics endpoint unavailable; waiting for the traefik overlay", "status", status)
		}
		return nil
	}
	s.edgeUnavailable = false

	perRouter, err := parseRouterCounters(body)
	if err != nil {
		return fmt.Errorf("parse traefik metrics: %w", err)
	}
	if s.lastEdge == nil {
		s.lastEdge = make(map[string]routerCounters)
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
	seen := make(map[string]struct{}, len(perRouter))
	for router, current := range perRouter {
		seen[router] = struct{}{}
		previous, known := s.lastEdge[router]
		s.lastEdge[router] = current
		if !known {
			continue
		}
		identity, ok := matchRouter(router, index)
		if !ok {
			continue
		}
		d := current.delta(previous)
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
	// Routers gone from the scrape (route removed, environment down) must
	// not resurrect stale baselines if a namesake returns later.
	for router := range s.lastEdge {
		if _, ok := seen[router]; !ok {
			delete(s.lastEdge, router)
		}
	}

	if len(order) == 0 {
		return nil
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
	return nil
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
