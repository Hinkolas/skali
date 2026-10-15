package kubernetes

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/manifest"
)

// redirectTarget applies a redirectRegex Middleware the way Traefik does:
// the regex runs against scheme://host[:port]/path?query and the
// replacement is the Location.
func redirectTarget(t *testing.T, middleware *unstructured.Unstructured, url string) string {
	t.Helper()
	require.NotNil(t, middleware)
	spec, _, err := unstructured.NestedMap(middleware.Object, "spec", "redirectRegex")
	require.NoError(t, err)
	require.Equal(t, true, spec["permanent"])
	pattern := regexp.MustCompile(spec["regex"].(string))
	require.True(t, pattern.MatchString(url), "an alias redirect matches every request: %s", url)
	return pattern.ReplaceAllString(url, spec["replacement"].(string))
}

func compileAliases(t *testing.T, routes string) *compiler.Result {
	t.Helper()
	document, err := manifest.Parse([]byte(`
name: aliases
applications:
  web:
    image: example.invalid/web:1
    ports:
      http:
        port: 8080
    routes:
`+routes), "skali.yml")
	require.NoError(t, err)
	result, err := compiler.Compile(document)
	require.NoError(t, err)
	return result
}

func renderedByName(t *testing.T, result *compiler.Result, options Options) map[string]*unstructured.Unstructured {
	t.Helper()
	objects, err := Render(result, options)
	require.NoError(t, err)
	byName := map[string]*unstructured.Unstructured{}
	for _, object := range objects {
		if typed, ok := object.(*unstructured.Unstructured); ok {
			byName[typed.GetKind()+"/"+typed.GetName()] = typed
		}
	}
	return byName
}

func firstRoute(t *testing.T, ingress *unstructured.Unstructured) map[string]any {
	t.Helper()
	require.NotNil(t, ingress)
	routes, _, err := unstructured.NestedSlice(ingress.Object, "spec", "routes")
	require.NoError(t, err)
	require.Len(t, routes, 1)
	return routes[0].(map[string]any)
}

// TestRenderRouteAliasesAutomatic: each alias of a `tls: automatic` route
// gets a websecure router with its own certificate and a plain-HTTP router
// that keeps the ACME path for the solver; both answer with a redirect to
// the route's domain, plain HTTP straight to https, a TLS request keeping
// a non-default port, path and query always. Every alias router carries
// its own hostname for claim retirement, and the route's routers keep
// theirs.
func TestRenderRouteAliasesAutomatic(t *testing.T) {
	t.Parallel()
	result := compileAliases(t, `
      public:
        domain: ${APP_DOMAIN}
        port: http
        aliases:
          - www.${APP_DOMAIN}
          - Old.Example.com
`)
	aliases := result.Definition.Applications["web"].Routes["public"].Aliases
	byName := renderedByName(t, result, Options{Namespace: "skali-aliases", Certificates: true,
		Variables: map[string]string{"APP_DOMAIN": "example.com"}})

	plain := byName["Middleware/"+AliasRedirectName("aliases", "web", "public", "http")]
	secure := byName["Middleware/"+AliasRedirectName("aliases", "web", "public", "primary")]
	require.Equal(t, "https://example.com/", redirectTarget(t, plain, "http://www.example.com/"))
	require.Equal(t, "https://example.com/a/b?c=d&e", redirectTarget(t, plain, "http://www.example.com/a/b?c=d&e"))
	require.Equal(t, "https://example.com/a", redirectTarget(t, plain, "http://www.example.com:8080/a"),
		"a scheme change drops the port, as the route's own redirect does")
	require.Equal(t, "https://example.com/a?b", redirectTarget(t, secure, "https://old.example.com/a?b"))
	require.Equal(t, "https://example.com:8443/a", redirectTarget(t, secure, "https://old.example.com:8443/a"),
		"the same scheme keeps the edge port")

	for index, host := range []string{"www.example.com", "old.example.com"} {
		alias := aliases[index]
		primary := byName["IngressRoute/"+AliasRouteName("aliases", "web", "public", alias, "primary")]
		route := firstRoute(t, primary)
		require.Equal(t, edge.HostMatch(host, "/"), route["match"])
		require.Equal(t, []string{secure.GetName()}, routeMiddlewares(t, primary), "an alias never compresses: it never serves")
		points, _, _ := unstructured.NestedStringSlice(primary.Object, "spec", "entryPoints")
		require.Equal(t, []string{edge.EntryPointWebSecure}, points)
		secret, _, _ := unstructured.NestedString(primary.Object, "spec", "tls", "secretName")
		require.Equal(t, AliasTLSName("aliases", "web", "public", alias), secret)
		require.Equal(t, host, primary.GetAnnotations()["skali.dev/route-hostname"])

		http := byName["IngressRoute/"+AliasRouteName("aliases", "web", "public", alias, "http")]
		require.Equal(t, edge.HTTPMatch(host, "/"), firstRoute(t, http)["match"], "the solver keeps the ACME path")
		require.Equal(t, []string{plain.GetName()}, routeMiddlewares(t, http))
		require.Equal(t, host, http.GetAnnotations()["skali.dev/route-hostname"])

		certificate := byName["Certificate/"+AliasTLSName("aliases", "web", "public", alias)]
		require.NotNil(t, certificate)
		names, _, _ := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames")
		require.Equal(t, []string{host}, names, "one certificate per alias, so one deferred alias holds back nothing else")
	}
	routeHTTP := byName["IngressRoute/"+RouteName("aliases", "web", "public", "http")]
	require.Equal(t, "example.com", routeHTTP.GetAnnotations()["skali.dev/route-hostname"])
	names, _, _ := unstructured.NestedStringSlice(byName["Certificate/"+RouteTLSName("aliases", "web", "public")].Object, "spec", "dnsNames")
	require.Equal(t, []string{"example.com"}, names, "the route's certificate does not grow alias names")

	// Names key on the alias's spelling, never its position: reordering the
	// list renders the same objects.
	reordered := renderedByName(t, compileAliases(t, `
      public:
        domain: ${APP_DOMAIN}
        port: http
        aliases:
          - Old.Example.com
          - www.${APP_DOMAIN}
`), Options{Namespace: "skali-aliases", Certificates: true, Variables: map[string]string{"APP_DOMAIN": "example.com"}})
	require.Equal(t, len(byName), len(reordered))
	for name := range byName {
		require.Contains(t, reordered, name)
	}
}

// TestRenderRouteAliasesPolicies: `optional` keeps the request's scheme
// (plain HTTP redirects to plain HTTP), `disabled` and an installation
// without certificates render one plain-HTTP router per alias and no
// certificate, and a route without aliases renders no alias objects.
func TestRenderRouteAliasesPolicies(t *testing.T) {
	t.Parallel()
	result := compileAliases(t, `
      relaxed:
        domain: relaxed.example.com
        port: http
        tls: optional
        aliases: [www.relaxed.example.com]
      internal:
        domain: internal.example.com
        port: http
        tls: disabled
        aliases: [www.internal.example.com]
      bare:
        domain: bare.example.com
        port: http
`)
	routes := result.Definition.Applications["web"].Routes
	byName := renderedByName(t, result, Options{Namespace: "skali-aliases", Certificates: true})

	relaxedPlain := byName["Middleware/"+AliasRedirectName("aliases", "web", "relaxed", "http")]
	require.Equal(t, "http://relaxed.example.com/a?b", redirectTarget(t, relaxedPlain, "http://www.relaxed.example.com/a?b"))
	require.Equal(t, "http://relaxed.example.com:8080/a", redirectTarget(t, relaxedPlain, "http://www.relaxed.example.com:8080/a"))
	require.Equal(t, "https://relaxed.example.com/a", redirectTarget(t,
		byName["Middleware/"+AliasRedirectName("aliases", "web", "relaxed", "primary")], "https://www.relaxed.example.com/a"))
	require.NotNil(t, byName["Certificate/"+AliasTLSName("aliases", "web", "relaxed", routes["relaxed"].Aliases[0])])

	internalAlias := routes["internal"].Aliases[0]
	internal := byName["IngressRoute/"+AliasRouteName("aliases", "web", "internal", internalAlias, "primary")]
	require.Equal(t, edge.HostMatch("www.internal.example.com", "/"), firstRoute(t, internal)["match"])
	points, _, _ := unstructured.NestedStringSlice(internal.Object, "spec", "entryPoints")
	require.Equal(t, []string{edge.EntryPointWeb}, points)
	require.Nil(t, byName["IngressRoute/"+AliasRouteName("aliases", "web", "internal", internalAlias, "http")])
	require.Nil(t, byName["Certificate/"+AliasTLSName("aliases", "web", "internal", internalAlias)])
	require.Nil(t, byName["Middleware/"+AliasRedirectName("aliases", "web", "internal", "primary")])
	require.Equal(t, "http://internal.example.com/a", redirectTarget(t,
		byName["Middleware/"+AliasRedirectName("aliases", "web", "internal", "http")], "http://www.internal.example.com/a"))

	require.Nil(t, byName["Middleware/"+AliasRedirectName("aliases", "web", "bare", "http")])

	// Without cert-manager every route serves plain HTTP, aliases included.
	local := renderedByName(t, result, Options{Namespace: "skali-aliases"})
	relaxedAlias := routes["relaxed"].Aliases[0]
	require.NotNil(t, local["IngressRoute/"+AliasRouteName("aliases", "web", "relaxed", relaxedAlias, "primary")])
	require.Nil(t, local["Certificate/"+AliasTLSName("aliases", "web", "relaxed", relaxedAlias)])
	require.Equal(t, "http://relaxed.example.com/a", redirectTarget(t,
		local["Middleware/"+AliasRedirectName("aliases", "web", "relaxed", "http")], "http://www.relaxed.example.com/a"))
}
