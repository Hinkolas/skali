// Package platform names the skalid-owned platform namespace and the
// in-cluster coordinates of the shared services environments reach across
// namespaces. It imports nothing of skali's, so the renderer, the
// substrate, and the bundle can all name the same Service without a
// dependency cycle.
package platform

import "slices"

// Namespace is the skalid-owned platform namespace holding every substrate
// pool, tenant object, and credential Secret.
const Namespace = "skali-platform"

// S3Service and S3Port address the shared S3 gateway (the SeaweedFS filer's
// embedded S3 API) inside Namespace. Bucket routes point the edge at it
// from environment namespaces.
const (
	S3Service = "seaweed-s3"
	S3Port    = 8333
)

// InternalS3Endpoint is the in-cluster S3 gateway URL every bucket
// publishes as internal_endpoint, and the endpoint of a bucket without a
// route. With one store per installation it is a constant.
func InternalS3Endpoint() string {
	return "http://" + S3Service + "." + Namespace + ".svc.cluster.local:8333"
}

// EnvironmentLabel is the namespace label carrying an environment's id; the
// platform's access policies admit claim holders by it. The renderer's
// label contract (kubernetes.LabelEnvironment) is this constant.
const EnvironmentLabel = "skali.dev/environment"

// The edge, as every network policy admits it: the Traefik pods k3s runs
// in kube-system, selected by their chart label. The environment policy
// admits them for routes and certificate validation, the S3 gateway's for
// bucket routes; the pair is decided once here.
const (
	EdgeNamespace = "kube-system"
	EdgePodLabel  = "app.kubernetes.io/name"
	EdgePodName   = "traefik"
)

// AccessPeers is what a platform port (a database pool, the S3 gateway)
// admits besides the platform's own fixed peers. The renderers turn each
// non-empty list into one ingress rule and render nothing for an empty
// one: an ingress rule without peers admits everyone.
type AccessPeers struct {
	// Environments are the ids of the environments holding a claim on the
	// port's service; their namespaces carry EnvironmentLabel.
	Environments []string
	// ProxyCIDRs are the /32 sources skalid's API-server service-proxy
	// traffic presents (kube.NodeProxyCIDRs): readiness probes and scrapes.
	ProxyCIDRs []string
	// HostExcept, when non-nil, admits every non-pod source on the port by
	// excluding these pod CIDRs from 0.0.0.0/0. Local platforms only: host
	// processes reach pools and the gateway through loopback NodePorts, and
	// the address that traffic presents inside the node is not a pod's.
	HostExcept []string
}

// EnvironmentIDs returns the holder ids sorted and deduplicated: several
// claims per environment are normal, and a stable order keeps server-side
// apply diffs empty.
func (p AccessPeers) EnvironmentIDs() []string {
	ids := slices.Clone(p.Environments)
	slices.Sort(ids)
	return slices.Compact(ids)
}
