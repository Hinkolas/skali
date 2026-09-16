package reconcile

import (
	"context"
	"strings"
	"time"

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
	// edgeProbeTimeout bounds one probe inside a pass.
	edgeProbeTimeout = 5 * time.Second
	// edgeProbeRetention evicts domains no pass has mentioned for this long
	// (a route removed from the manifest).
	edgeProbeRetention = time.Hour
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

// observeDomain returns the cached verdict for domain, re-probing when the
// cache is older than interval. The lock is released during the network
// call so one slow domain never parks another environment's pass; two
// workers probing the same domain at once is harmless.
func (k *Kernel) observeDomain(ctx context.Context, certName, domain string, now time.Time, interval time.Duration) (edgeprobe.Result, bool) {
	k.domainMu.Lock()
	for key, entry := range k.domains {
		if now.Sub(entry.lastSeen) > edgeProbeRetention {
			delete(k.domains, key)
		}
	}
	k.certDomains[certName] = domain
	entry, known := k.domains[domain]
	entry.lastSeen = now
	k.domains[domain] = entry
	stale := !known || now.Sub(entry.result.CheckedAt) >= interval
	k.domainMu.Unlock()
	if !stale {
		return entry.result, entry.arrived
	}

	probeCtx, cancel := context.WithTimeout(ctx, edgeProbeTimeout)
	result := k.deps.ProbeDomain(probeCtx, domain)
	cancel()
	if result.CheckedAt.IsZero() {
		result.CheckedAt = now
	}

	k.domainMu.Lock()
	defer k.domainMu.Unlock()
	entry = k.domains[domain]
	if result.State == edgeprobe.StateReachable && (!known || entry.result.State != edgeprobe.StateReachable) {
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

// edgeStatus is the read-only projection of the cached verdict for one
// route certificate; nil when no pass has probed its domain.
func (k *Kernel) edgeStatus(certName string) *EdgeStatus {
	k.domainMu.Lock()
	defer k.domainMu.Unlock()
	domain, ok := k.certDomains[certName]
	if !ok {
		return nil
	}
	entry, ok := k.domains[domain]
	if !ok || entry.result.State == "" {
		return nil
	}
	return &EdgeStatus{
		Domain:    domain,
		State:     string(entry.result.State),
		Message:   entry.result.Message,
		CheckedAt: entry.result.CheckedAt,
		Addresses: edgeAddressLines(entry.result.Addresses),
	}
}

// edgeResources synthesizes the KindEdge resources for one application's
// TLS routes from the cache, for the module's certificate gate.
func (k *Kernel) edgeResources(definition compiler.ProjectDefinition, key string) []module.ObservedResource {
	if k.deps.ProbeDomain == nil {
		return nil
	}
	application, ok := definition.Applications[key]
	if !ok {
		return nil
	}
	var resources []module.ObservedResource
	for _, routeKey := range utils.SortedKeys(application.Routes) {
		if application.Routes[routeKey].TLS == "disabled" {
			continue
		}
		name := rendering.RouteTLSName(definition.Name, key, routeKey)
		status := k.edgeStatus(name)
		if status == nil {
			continue
		}
		resources = append(resources, module.ObservedResource{
			Kind: module.KindEdge, Name: name,
			Edge: &module.EdgeReach{
				Domain:   status.Domain,
				State:    status.State,
				Deferred: edgeprobe.State(status.State).Pending(),
			},
		})
	}
	return resources
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
