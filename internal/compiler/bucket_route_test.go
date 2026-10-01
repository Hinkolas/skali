package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/manifest"
)

func compileBucketRouteSource(t *testing.T, buckets string) (*Result, error) {
	t.Helper()
	source := "name: routes\nbuckets:\n" + buckets
	document, err := manifest.Parse([]byte(source), "skali.yml")
	require.NoError(t, err)
	return Compile(document)
}

// TestCompileBucketRoute: a declared route compiles to its canonical
// domain expression with the TLS policy defaulted, variables are
// collected like an application route's, two buckets may share a
// hostname, and the mistakes the edge would otherwise serve (a bad
// hostname, an unknown policy, a missing domain) are compile-time
// diagnostics.
func TestCompileBucketRoute(t *testing.T) {
	t.Parallel()
	none, err := compileBucketRouteSource(t, "  files: {}\n")
	require.NoError(t, err)
	require.Nil(t, none.Definition.Buckets["files"].Route, "nothing declared keeps the bucket in-cluster")

	literal, err := compileBucketRouteSource(t, "  files:\n    route:\n      domain: Files.Example.com\n")
	require.NoError(t, err)
	route := literal.Definition.Buckets["files"].Route
	require.NotNil(t, route)
	require.Equal(t, "Files.Example.com", route.Domain.Literal(), "the expression keeps the source; canonicalization happens at resolve time")
	require.Equal(t, "automatic", route.TLS)
	require.Empty(t, literal.Definition.RequiredVariables)

	variable, err := compileBucketRouteSource(t, "  files:\n    route:\n      domain: ${STORAGE_DOMAIN}\n      tls: optional\n")
	require.NoError(t, err)
	route = variable.Definition.Buckets["files"].Route
	require.True(t, route.Domain.HasProjectVariables())
	require.Equal(t, "optional", route.TLS)
	require.Len(t, variable.Definition.RequiredVariables, 1)
	require.Equal(t, "STORAGE_DOMAIN", variable.Definition.RequiredVariables[0].Name)

	shared, err := compileBucketRouteSource(t, "  files:\n    route:\n      domain: s3.example.com\n  avatars:\n    route:\n      domain: s3.example.com\n")
	require.NoError(t, err, "buckets may share a hostname; the edge keys each on its bucket path")
	require.NotNil(t, shared.Definition.Buckets["avatars"].Route)

	for name, source := range map[string]string{
		"missing domain": "  files:\n    route:\n      tls: automatic\n",
		"bad hostname":   "  files:\n    route:\n      domain: 'not a host'\n",
		"host with port": "  files:\n    route:\n      domain: files.example.com:8443\n",
		"unknown tls":    "  files:\n    route:\n      domain: files.example.com\n      tls: sometimes\n",
		"output ref":     "  files:\n    route:\n      domain: '{{buckets.files.endpoint}}'\n",
	} {
		_, err := compileBucketRouteSource(t, source)
		require.Error(t, err, name)
	}
}

// TestResolveBucketRoutes: bucket routes join the resolved hostname list
// (so admission claims them), resolve project variables, and refuse a
// hostname an application route resolves to, even when the two differ as
// expressions.
func TestResolveBucketRoutes(t *testing.T) {
	t.Parallel()
	document, err := manifest.Parse([]byte(`
name: routes
applications:
  web:
    image: example.invalid/web:1
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: ${APP_DOMAIN}
        port: http
buckets:
  files:
    route:
      domain: ${STORAGE_DOMAIN}
  avatars:
    route:
      domain: Static.Example.com
      tls: disabled
`), "skali.yml")
	require.NoError(t, err)
	result, err := Compile(document)
	require.NoError(t, err)

	routes, err := ResolveRoutes(result.Definition, map[string]string{
		"APP_DOMAIN": "app.example.com", "STORAGE_DOMAIN": "files.example.com",
	})
	require.NoError(t, err)
	require.Len(t, routes, 3)
	require.Equal(t, ResolvedRoute{Application: "web", Key: "public", Domain: "app.example.com", Path: "/", Field: "applications.web.routes.public"}, routes[0])
	require.Equal(t, ResolvedRoute{Bucket: "avatars", Key: "route", Domain: "static.example.com", Path: "/", Field: "buckets.avatars.route"}, routes[1])
	require.Equal(t, ResolvedRoute{Bucket: "files", Key: "route", Domain: "files.example.com", Path: "/", Field: "buckets.files.route"}, routes[2])

	_, err = ResolveRoutes(result.Definition, map[string]string{
		"APP_DOMAIN": "shared.example.com", "STORAGE_DOMAIN": "shared.example.com",
	})
	var routeErr *RouteError
	require.ErrorAs(t, err, &routeErr)
	require.Equal(t, "buckets.files.route", routeErr.Field)
	require.Contains(t, routeErr.Detail, "conflicts with applications.web.routes.public")
	require.NotContains(t, routeErr.Detail, "shared.example.com", "resolved values stay out of errors")

	_, err = ResolveRoutes(result.Definition, map[string]string{
		"APP_DOMAIN": "app.example.com", "STORAGE_DOMAIN": "bad host",
	})
	require.ErrorAs(t, err, &routeErr)
	require.Equal(t, "buckets.files.route.domain", routeErr.Field)
	require.Contains(t, routeErr.Detail, "resolved from ${STORAGE_DOMAIN}")
}
