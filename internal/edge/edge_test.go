package edge

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestIngressRouteShape(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"skali.dev/managed": "true"}
	route := IngressRoute("proj-env", "web-public", labels,
		[]string{EntryPointWebSecure},
		[]Route{{
			Match:       HostMatch("app.example.com", "/"),
			Service:     Service{Name: "web", PortName: "http", Strategy: StrategyP2C},
			Middlewares: []string{RedirectMiddlewareName},
		}},
		"web-public-tls")

	require.Equal(t, IngressRouteGVK, route.GroupVersionKind())
	require.Equal(t, "web-public", route.GetName())
	require.Equal(t, "proj-env", route.GetNamespace())
	require.Equal(t, labels, route.GetLabels())

	points, _, err := unstructured.NestedStringSlice(route.Object, "spec", "entryPoints")
	require.NoError(t, err)
	require.Equal(t, []string{"websecure"}, points)

	routes, _, err := unstructured.NestedSlice(route.Object, "spec", "routes")
	require.NoError(t, err)
	require.Len(t, routes, 1)
	rule := routes[0].(map[string]any)
	require.Equal(t, `Host("app.example.com") && PathPrefix("/")`, rule["match"])
	require.Equal(t, "Rule", rule["kind"])
	service := rule["services"].([]any)[0].(map[string]any)
	require.Equal(t, "web", service["name"])
	require.Equal(t, "http", service["port"])
	require.Equal(t, "p2c", service["strategy"])
	middleware := rule["middlewares"].([]any)[0].(map[string]any)
	require.Equal(t, "redirect-https", middleware["name"])

	secret, _, err := unstructured.NestedString(route.Object, "spec", "tls", "secretName")
	require.NoError(t, err)
	require.Equal(t, "web-public-tls", secret)
}

func TestIngressRouteDefaults(t *testing.T) {
	t.Parallel()
	route := IngressRoute("proj-env", "web-public", nil,
		[]string{EntryPointWeb},
		[]Route{{
			Match:   HostMatch("app.example.com", "/api"),
			Service: Service{Name: "web", PortNumber: 8080},
		}},
		"")

	rule := mustRoutes(t, route)[0].(map[string]any)
	service := rule["services"].([]any)[0].(map[string]any)
	require.Equal(t, int64(8080), service["port"])
	_, hasStrategy := service["strategy"]
	require.False(t, hasStrategy, "round-robin stays Traefik's default, never rendered")
	_, hasMiddlewares := rule["middlewares"]
	require.False(t, hasMiddlewares)
	_, hasTLS, err := unstructured.NestedMap(route.Object, "spec", "tls")
	require.NoError(t, err)
	require.False(t, hasTLS)
}

func TestRedirectMiddleware(t *testing.T) {
	t.Parallel()
	middleware := RedirectMiddleware("proj-env", map[string]string{"skali.dev/managed": "true"})
	require.Equal(t, MiddlewareGVK, middleware.GroupVersionKind())
	require.Equal(t, RedirectMiddlewareName, middleware.GetName())
	scheme, _, err := unstructured.NestedString(middleware.Object, "spec", "redirectScheme", "scheme")
	require.NoError(t, err)
	require.Equal(t, "https", scheme)
	permanent, _, err := unstructured.NestedBool(middleware.Object, "spec", "redirectScheme", "permanent")
	require.NoError(t, err)
	require.True(t, permanent)
}

func TestCertificate(t *testing.T) {
	t.Parallel()
	certificate := Certificate("proj-env", "web-public-tls", "app.example.com", nil)
	require.Equal(t, CertificateGVK, certificate.GroupVersionKind())
	require.Equal(t, "web-public-tls", certificate.GetName())
	secret, _, err := unstructured.NestedString(certificate.Object, "spec", "secretName")
	require.NoError(t, err)
	require.Equal(t, "web-public-tls", secret, "certificate and secret share one name")
	names, _, err := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames")
	require.NoError(t, err)
	require.Equal(t, []string{"app.example.com"}, names)
	issuer, _, err := unstructured.NestedString(certificate.Object, "spec", "issuerRef", "name")
	require.NoError(t, err)
	require.Equal(t, IssuerName, issuer)
	kind, _, err := unstructured.NestedString(certificate.Object, "spec", "issuerRef", "kind")
	require.NoError(t, err)
	require.Equal(t, "ClusterIssuer", kind)
}

func mustRoutes(t *testing.T, object *unstructured.Unstructured) []any {
	t.Helper()
	routes, _, err := unstructured.NestedSlice(object.Object, "spec", "routes")
	require.NoError(t, err)
	return routes
}
