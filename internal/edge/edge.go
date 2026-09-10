// Package edge is the shared vocabulary of the managed Traefik edge and its
// cert-manager issuance: the CRD coordinates, the entrypoint names of the
// k3s-packaged Traefik, and unstructured builders for the objects skali
// renders against them. It stays free of skali imports so the renderer, the
// bundle, and the substrate can all speak it.
package edge

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strconv"
)

var (
	IngressRouteGVK = schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "IngressRoute"}
	MiddlewareGVK   = schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "Middleware"}
	CertificateGVK  = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}

	IngressRouteGVR = schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}
	MiddlewareGVR   = schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "middlewares"}
	CertificateGVR  = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
)

const (
	// EntryPointWeb and EntryPointWebSecure are the entrypoint names the
	// k3s-packaged Traefik binds to ports 80 and 443; the names are k3s
	// chart contract.
	EntryPointWeb       = "web"
	EntryPointWebSecure = "websecure"
	// IssuerName is the ClusterIssuer every issued route certificate binds
	// to. The bundle renders the issuer under this name, so it is contract.
	IssuerName = "skali"
	// StrategyP2C is Traefik's power-of-two-choices balancer: each request
	// goes to the less loaded of two random replicas. It is what the
	// manifest's least-requests strategy compiles to.
	StrategyP2C = "p2c"
	// RedirectMiddlewareName names the shared per-namespace redirectScheme
	// Middleware that answers plain HTTP on TLS routes.
	RedirectMiddlewareName = "redirect-https"
)

// Service is one IngressRoute backend: a Kubernetes Service port addressed
// by name or number, with an optional non-default balancing strategy.
type Service struct {
	Name       string
	PortName   string
	PortNumber int
	// Strategy is a Traefik balancer name such as StrategyP2C; empty keeps
	// Traefik's weighted round-robin default.
	Strategy string
}

// Route is one router inside an IngressRoute. Traefik prioritizes routers
// by match-rule length, so longer (more specific) rules win without
// explicit priorities.
type Route struct {
	Match       string
	Service     Service
	Middlewares []string
}

// HostMatch builds the canonical router rule for one public route.
func HostMatch(domain, path string) string {
	return "Host(" + strconv.Quote(domain) + ") && PathPrefix(" + strconv.Quote(path) + ")"
}

// HTTPMatch reserves HTTP-01 for the solver, independently of router
// priority and whether a usable HTTPS certificate exists yet.
func HTTPMatch(domain, path string) string {
	return HostMatch(domain, path) + ` && !PathPrefix("/.well-known/acme-challenge/")`
}

// IngressRoute builds a traefik.io/v1alpha1 IngressRoute. A non-empty
// tlsSecret makes the routers TLS-only on their entrypoints.
func IngressRoute(namespace, name string, labels map[string]string, entryPoints []string, routes []Route, tlsSecret string) *unstructured.Unstructured {
	specRoutes := make([]any, 0, len(routes))
	for _, route := range routes {
		service := map[string]any{"name": route.Service.Name}
		if route.Service.PortName != "" {
			service["port"] = route.Service.PortName
		} else {
			service["port"] = int64(route.Service.PortNumber)
		}
		if route.Service.Strategy != "" {
			service["strategy"] = route.Service.Strategy
		}
		specRoute := map[string]any{
			"match":    route.Match,
			"kind":     "Rule",
			"services": []any{service},
		}
		if len(route.Middlewares) > 0 {
			middlewares := make([]any, 0, len(route.Middlewares))
			for _, middleware := range route.Middlewares {
				middlewares = append(middlewares, map[string]any{"name": middleware})
			}
			specRoute["middlewares"] = middlewares
		}
		specRoutes = append(specRoutes, specRoute)
	}
	points := make([]any, 0, len(entryPoints))
	for _, point := range entryPoints {
		points = append(points, point)
	}
	spec := map[string]any{
		"entryPoints": points,
		"routes":      specRoutes,
	}
	if tlsSecret != "" {
		spec["tls"] = map[string]any{"secretName": tlsSecret}
	}
	return object(IngressRouteGVK, namespace, name, labels, spec)
}

// RedirectMiddleware builds the shared redirectScheme Middleware answering
// plain HTTP on TLS routes with a permanent redirect.
func RedirectMiddleware(namespace string, labels map[string]string) *unstructured.Unstructured {
	return object(MiddlewareGVK, namespace, RedirectMiddlewareName, labels, map[string]any{
		"redirectScheme": map[string]any{
			"scheme":    "https",
			"permanent": true,
		},
	})
}

// Certificate builds a cert-manager.io/v1 Certificate bound to the managed
// ClusterIssuer. Following cert-manager convention the Certificate carries
// the name of the Secret it issues into.
func Certificate(namespace, name, domain string, labels map[string]string) *unstructured.Unstructured {
	return object(CertificateGVK, namespace, name, labels, map[string]any{
		"secretName": name,
		"dnsNames":   []any{domain},
		"issuerRef": map[string]any{
			"kind":  "ClusterIssuer",
			"name":  IssuerName,
			"group": "cert-manager.io",
		},
	})
}

func object(gvk schema.GroupVersionKind, namespace, name string, labels map[string]string, spec map[string]any) *unstructured.Unstructured {
	metadata := map[string]any{
		"name":      name,
		"namespace": namespace,
	}
	if len(labels) > 0 {
		converted := make(map[string]any, len(labels))
		for key, value := range labels {
			converted[key] = value
		}
		metadata["labels"] = converted
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata":   metadata,
		"spec":       spec,
	}}
}
