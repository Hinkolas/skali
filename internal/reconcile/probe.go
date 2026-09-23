package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/edge/edgeprobe"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/utils"
)

// ErrNoEdgeProbe: the installation runs without an edge probe (the local
// platform, where *.localhost cannot be resolved from inside the cluster,
// or a bare skalid without cert-manager), so no route domain is ever probed.
var ErrNoEdgeProbe = errors.New("reconcile: this installation has no edge probe")

// RouteProbe is one manual edge probe of a route domain.
type RouteProbe struct {
	Service string
	Key     string
	Domain  string
	Result  edgeprobe.Result

	certificate string
}

// ProbeRoutes probes every TLS route domain of the environment's target
// revision right now, ahead of the reconciler's own cadence, and reports
// the verdicts with their per-address detail. The results replace the
// cached ones, so the next pass (enqueued here) acts on them: a route
// without a usable certificate whose domain answers as this installation
// counts as arrived, which requests one fresh issuance even when the
// domain was already here and cert-manager is waiting out a backoff.
func (k *Kernel) ProbeRoutes(ctx context.Context, environmentID uuid.UUID) ([]RouteProbe, error) {
	if k.deps.ProbeDomain == nil {
		return nil, ErrNoEdgeProbe
	}
	_, rev, err := k.targetRevision(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		return nil, nil
	}
	probes, err := k.routeDomains(ctx, environmentID, rev)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var group sync.WaitGroup
	for i := range probes {
		group.Add(1)
		go func(probe *RouteProbe) {
			defer group.Done()
			probeCtx, cancel := context.WithTimeout(ctx, edgeProbeTimeout)
			result := k.deps.ProbeDomain(probeCtx, probe.Domain)
			cancel()
			k.domainMu.Lock()
			usable := k.routes[routeKeyOf(environmentID, probe.certificate)].usable
			k.domainMu.Unlock()
			probe.Result, _ = k.recordProbe(probe.Domain, result, now, !usable)
		}(&probes[i])
	}
	group.Wait()
	k.Enqueue(environmentID)
	if k.deps.Observed != nil {
		k.deps.Observed.Invalidate(environmentID)
	}
	return probes, nil
}

// targetRevision loads the environment's target and decodes its target
// revision; rev is nil when the environment has no target yet.
func (k *Kernel) targetRevision(ctx context.Context, environmentID uuid.UUID) (store.EnvironmentTarget, *revision.Revision, error) {
	target, err := k.deps.Store.GetEnvironmentTarget(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return target, nil, ErrEnvironmentNotFound
		}
		return target, nil, fmt.Errorf("reconcile: get target: %w", err)
	}
	if target.TargetRevisionID == nil {
		return target, nil, nil
	}
	row, err := k.deps.Store.GetRevisionByID(ctx, *target.TargetRevisionID)
	if err != nil {
		return target, nil, fmt.Errorf("reconcile: get target revision: %w", err)
	}
	rev, err := revision.Decode(row.Document)
	if err != nil {
		return target, nil, err
	}
	return target, rev, nil
}

// routeDomains resolves every TLS route of rev to its canonical domain and
// Certificate name, the same way rendering fills the Certificate's
// dnsNames, so the strings match what a pass judges under the lock.
func (k *Kernel) routeDomains(ctx context.Context, environmentID uuid.UUID, rev *revision.Revision) ([]RouteProbe, error) {
	variables, err := k.routeVariables(ctx, environmentID, rev)
	if err != nil {
		return nil, err
	}
	var probes []RouteProbe
	for _, appKey := range utils.SortedKeys(rev.Definition.Applications) {
		application := rev.Definition.Applications[appKey]
		for _, routeKey := range utils.SortedKeys(application.Routes) {
			route := application.Routes[routeKey]
			if route.TLS == "disabled" {
				continue
			}
			domain, err := compiler.ResolveExpression(route.Domain, variables)
			if err != nil {
				return nil, fmt.Errorf("reconcile: route %s/%s domain: %w", appKey, routeKey, err)
			}
			domain, err = edge.CanonicalDomain(domain)
			if err != nil {
				return nil, fmt.Errorf("reconcile: route %s/%s domain: %w", appKey, routeKey, err)
			}
			probes = append(probes, RouteProbe{Service: appKey, Key: routeKey, Domain: domain,
				certificate: rendering.RouteTLSName(rev.Definition.Name, appKey, routeKey)})
		}
	}
	return probes, nil
}
