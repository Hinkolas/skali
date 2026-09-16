package reconcile

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/edge/edgeprobe"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/utils"
)

const (
	// edgeProbeRolloutInterval is the re-probe cadence, and the requeue,
	// while a rollout run is attached and one of its domains is pending:
	// the operator is watching, and the moment DNS moves the certificate
	// should follow within the same transcript.
	edgeProbeRolloutInterval = 30 * time.Second
	// edgeProbeIdleInterval is the cadence for a converged environment
	// whose domain is still elsewhere; a migration waits days, so the pass
	// cost is kept low while issuance still starts within minutes of the
	// DNS change.
	edgeProbeIdleInterval = 2 * time.Minute
	// edgeProbeTimeout bounds one probe.
	edgeProbeTimeout = 5 * time.Second
	// edgeProbeMissRequeue is the requeue when a pass finds no verdict for
	// a route domain (the route appeared after the pre-lock probe phase, or
	// a usable certificate just stopped covering it): the next pass probes
	// the domain before it takes the environment lock.
	edgeProbeMissRequeue = time.Second
	// edgeProbeRetention evicts domains no pass has mentioned for this long
	// (a route removed from the manifest).
	edgeProbeRetention = time.Hour
	// issuedNamesTimeout bounds one read of a TLS Secret's annotations.
	issuedNamesTimeout = 5 * time.Second
)

// domainProbe is one cached edge verdict.
type domainProbe struct {
	result   edgeprobe.Result
	lastSeen time.Time
	// arrived is set when a probe finds the domain reachable after it was
	// not (or was never probed); the TLS pass clears it once issuance is
	// under way, so one arrival triggers at most one retry.
	arrived bool
}

// routeKey identifies one route certificate. The Certificate name hashes
// project, application and route only, so two environments of one project
// share it; the environment keeps their verdicts apart.
type routeKey struct {
	environment uuid.UUID
	certificate string
}

// routeRecord is what the last pass concluded about one route certificate:
// the domain it wants, whether the certificate is deferred (the domain does
// not reach this edge and nothing usable is on hand) or mismatched (a valid
// certificate exists, issued for other names), the issued names read for
// the certificate's current notAfter, and the arrival whose issuance
// outcome is still owed a run.
type routeRecord struct {
	domain    string
	usable    bool
	deferred  bool
	mismatch  bool
	issued    []string
	issuedFor time.Time
	awaiting  time.Time
}

// lookupDomain is the cache half of an edge verdict: never network, so a
// pass may call it while holding the environment lock. It evicts domains
// no pass has mentioned within the retention and marks this one seen.
// known reports that a verdict exists, fresh that it is younger than
// interval.
func (k *Kernel) lookupDomain(domain string, now time.Time, interval time.Duration) (result edgeprobe.Result, arrived, known, fresh bool) {
	k.domainMu.Lock()
	defer k.domainMu.Unlock()
	for key, entry := range k.domains {
		if now.Sub(entry.lastSeen) > edgeProbeRetention {
			delete(k.domains, key)
		}
	}
	entry, known := k.domains[domain]
	if !known {
		return edgeprobe.Result{}, false, false, false
	}
	entry.lastSeen = now
	k.domains[domain] = entry
	return entry.result, entry.arrived, true, now.Sub(entry.result.CheckedAt) < interval
}

// probeDomain is the network half: one bounded probe, recorded in the
// cache. Two workers probing the same domain at once is harmless.
func (k *Kernel) probeDomain(ctx context.Context, domain string, now time.Time) (edgeprobe.Result, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, edgeProbeTimeout)
	result := k.deps.ProbeDomain(probeCtx, domain)
	cancel()
	return k.recordProbe(domain, result, now, false)
}

// probeDue refreshes, before the environment lock is taken, the verdict of
// every route domain the pass will judge whose cached verdict is missing or
// older than interval. A route whose last pass proved a usable certificate
// for the same domain is skipped: the pass will not ask. Probes run in
// parallel, each bounded by edgeProbeTimeout, so a pass never holds the
// lock through network waits and Promote is not parked behind them.
func (k *Kernel) probeDue(ctx context.Context, environmentID uuid.UUID, routes []RouteProbe, now time.Time, interval time.Duration) {
	var group sync.WaitGroup
	queued := make(map[string]bool, len(routes))
	for _, route := range routes {
		if route.Domain == "" || queued[route.Domain] {
			continue
		}
		k.domainMu.Lock()
		record := k.routes[routeKeyOf(environmentID, route.certificate)]
		k.domainMu.Unlock()
		if record.usable && record.domain == route.Domain {
			continue
		}
		if _, _, known, fresh := k.lookupDomain(route.Domain, now, interval); known && fresh {
			continue
		}
		queued[route.Domain] = true
		group.Add(1)
		go func(domain string) {
			defer group.Done()
			k.probeDomain(ctx, domain, now)
		}(route.Domain)
	}
	group.Wait()
}

// recordProbe stores one probe result. The arrival flag is raised on the
// transition to reachable (or a first reachable verdict); force raises it
// on any reachable verdict, which is how a manual probe asks for a fresh
// issuance of a route whose domain was already here.
func (k *Kernel) recordProbe(domain string, result edgeprobe.Result, now time.Time, force bool) (edgeprobe.Result, bool) {
	if result.CheckedAt.IsZero() {
		result.CheckedAt = now
	}
	k.domainMu.Lock()
	defer k.domainMu.Unlock()
	entry, known := k.domains[domain]
	if result.State == edgeprobe.StateReachable && (force || !known || entry.result.State != edgeprobe.StateReachable) {
		entry.arrived = true
	}
	entry.result = result
	entry.lastSeen = now
	k.domains[domain] = entry
	return entry.result, entry.arrived
}

// settleArrival records that the arrival of domain was acted on.
func (k *Kernel) settleArrival(domain string) {
	k.domainMu.Lock()
	defer k.domainMu.Unlock()
	if entry, ok := k.domains[domain]; ok {
		entry.arrived = false
		k.domains[domain] = entry
	}
}

// issuedNames returns the names the certificate's Secret was issued for,
// cached per certificate for as long as its notAfter stands (a new
// issuance moves it). nil means no verdict: no reader wired, no
// annotation, or a read that failed (logged, retried next pass).
func (k *Kernel) issuedNames(ctx context.Context, key routeKey, namespace, secretName string, notAfter time.Time) []string {
	if k.deps.IssuedNames == nil || notAfter.IsZero() {
		return nil
	}
	k.domainMu.Lock()
	record, ok := k.routes[key]
	k.domainMu.Unlock()
	if ok && !record.issuedFor.IsZero() && record.issuedFor.Equal(notAfter) {
		return record.issued
	}
	readCtx, cancel := context.WithTimeout(ctx, issuedNamesTimeout)
	names, err := k.deps.IssuedNames(readCtx, namespace, secretName)
	cancel()
	if err != nil {
		slog.WarnContext(ctx, "reading the issued names of a route certificate failed", "environment_id", key.environment, "certificate", key.certificate, "error", err)
		return nil
	}
	k.domainMu.Lock()
	defer k.domainMu.Unlock()
	record = k.routes[key]
	record.issued, record.issuedFor = names, notAfter
	k.routes[key] = record
	return names
}

// noteRoute records the pass's conclusion about one route certificate. A
// verdict that flipped nudges the status stream, so the console repaints
// without waiting for the next observation event.
func (k *Kernel) noteRoute(key routeKey, domain string, usable, deferred, mismatch bool) routeRecord {
	k.domainMu.Lock()
	record, known := k.routes[key]
	changed := !known || record.deferred != deferred || record.mismatch != mismatch
	record.domain, record.usable, record.deferred, record.mismatch = domain, usable, deferred, mismatch
	k.routes[key] = record
	k.domainMu.Unlock()
	if changed && k.deps.Observed != nil {
		k.deps.Observed.Invalidate(key.environment)
	}
	return record
}

// awaitIssuance marks (or, with a zero time, clears) the arrival whose
// issuance outcome the next converged passes narrate.
func (k *Kernel) awaitIssuance(key routeKey, at time.Time) {
	k.domainMu.Lock()
	defer k.domainMu.Unlock()
	record := k.routes[key]
	record.awaiting = at
	k.routes[key] = record
}

// edgeStatus is the read-only projection of the cached verdict for one
// route certificate; nil when no pass has probed its domain.
func (k *Kernel) edgeStatus(environmentID uuid.UUID, certName string) *EdgeStatus {
	k.domainMu.Lock()
	defer k.domainMu.Unlock()
	record, ok := k.routes[routeKey{environmentID, certName}]
	if !ok {
		return nil
	}
	entry, ok := k.domains[record.domain]
	if !ok || entry.result.State == "" {
		return nil
	}
	return &EdgeStatus{
		Domain:    record.domain,
		State:     string(entry.result.State),
		Message:   entry.result.Message,
		CheckedAt: entry.result.CheckedAt,
		Addresses: edgeAddressLines(entry.result.Addresses),
		Deferred:  record.deferred,
	}
}

// edgeResources synthesizes the KindEdge resources for one application's
// TLS routes from the cache, for the module's certificate gate. A route
// the pass never judged yields nothing; one it judged before any probe
// (a mismatch found while the prober is absent) carries an empty state.
func (k *Kernel) edgeResources(environmentID uuid.UUID, definition compiler.ProjectDefinition, key string) []module.ObservedResource {
	application, ok := definition.Applications[key]
	if !ok {
		return nil
	}
	k.domainMu.Lock()
	defer k.domainMu.Unlock()
	var resources []module.ObservedResource
	for _, routeKey := range utils.SortedKeys(application.Routes) {
		if application.Routes[routeKey].TLS == "disabled" {
			continue
		}
		name := rendering.RouteTLSName(definition.Name, key, routeKey)
		record, ok := k.routes[routeKeyOf(environmentID, name)]
		if !ok {
			continue
		}
		state := ""
		if entry, ok := k.domains[record.domain]; ok {
			state = string(entry.result.State)
		}
		resources = append(resources, module.ObservedResource{
			Kind: module.KindEdge, Name: name,
			Edge: &module.EdgeReach{
				Domain:   record.domain,
				State:    state,
				Deferred: record.deferred,
				Mismatch: record.mismatch,
				Issued:   record.issued,
			},
		})
	}
	return resources
}

func routeKeyOf(environmentID uuid.UUID, certName string) routeKey {
	return routeKey{environment: environmentID, certificate: certName}
}

// edgeAddressLines renders one line per probed address for journals and
// status surfaces: "203.0.113.10: answered by another server (HTTP 301
// without Skali-Instance)".
func edgeAddressLines(addresses []edgeprobe.AddressResult) []string {
	lines := make([]string, 0, len(addresses))
	for _, address := range addresses {
		var verdict string
		switch address.Outcome {
		case edgeprobe.OutcomeOurs:
			verdict = "answered by this installation"
		case edgeprobe.OutcomeForeign:
			verdict = "answered by another server"
		default:
			verdict = "did not answer"
		}
		line := address.Address + ": " + verdict
		if address.Detail != "" {
			line += " (" + address.Detail + ")"
		}
		lines = append(lines, line)
	}
	return lines
}

// edgeAddressField joins the address lines for one journal field.
func edgeAddressField(addresses []edgeprobe.AddressResult) string {
	return strings.Join(edgeAddressLines(addresses), "\n")
}
