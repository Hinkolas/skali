package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/manifest"
)

func compileAliasSource(t *testing.T, source string) (*Result, error) {
	t.Helper()
	document, err := manifest.Parse([]byte(source), "skali.yml")
	require.NoError(t, err)
	return Compile(document)
}

// aliasApp is one application whose public route declares the given alias
// lines; extra is appended to the project unchanged.
func aliasApp(aliases, extra string) string {
	return `
name: aliases
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
        aliases:
` + aliases + extra
}

// TestCompileRouteAliases: aliases compile to expressions in manifest
// order with their variables collected, a route without aliases keeps the
// definition JSON (and so its hash) free of the field, and the mistakes
// the edge would otherwise serve are compile-time diagnostics.
func TestCompileRouteAliases(t *testing.T) {
	t.Parallel()
	result, err := compileAliasSource(t, aliasApp("          - www.${APP_DOMAIN}\n          - Old-Brand.example.com\n          - ${LEGACY_DOMAIN}\n", ""))
	require.NoError(t, err)
	route := result.Definition.Applications["web"].Routes["public"]
	require.Len(t, route.Aliases, 3)
	require.Equal(t, "www.${APP_DOMAIN}", route.Aliases[0].Source())
	require.Equal(t, "Old-Brand.example.com", route.Aliases[1].Literal(), "the expression keeps the source; canonicalization happens at resolve time")
	names := []string{}
	for _, variable := range result.Definition.RequiredVariables {
		names = append(names, variable.Name)
	}
	require.ElementsMatch(t, []string{"APP_DOMAIN", "LEGACY_DOMAIN"}, names)

	plain, err := compileAliasSource(t, `
name: aliases
applications:
  web:
    image: example.invalid/web:1
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: example.com
        port: http
`)
	require.NoError(t, err)
	require.Nil(t, plain.Definition.Applications["web"].Routes["public"].Aliases)

	for name, test := range map[string]struct{ source, message string }{
		"empty":          {aliasApp("          - ''\n", ""), "aliases[0]: must not be empty"},
		"bad hostname":   {aliasApp("          - 'not a host'\n", ""), "aliases[0]:"},
		"host with port": {aliasApp("          - www.example.com:8443\n", ""), "aliases[0]:"},
		"output ref":     {aliasApp("          - '{{databases.db.host}}'\n", ""), "aliases[0]:"},
		"duplicate":      {aliasApp("          - www.example.com\n          - www.example.com\n", ""), "aliases[1]: conflicts with alias applications.web.routes.public.aliases[0]"},
		"own domain":     {aliasApp("          - ${APP_DOMAIN}\n", ""), "aliases[0]: conflicts with route applications.web.routes.public;"},
		"other route": {aliasApp("          - api.example.com\n", `
  api:
    image: example.invalid/api:1
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: api.example.com
        path: /v1
        port: http
`), "aliases[0]: conflicts with route applications.api.routes.public;"},
		"other alias": {aliasApp("          - www.example.com\n", `
  api:
    image: example.invalid/api:1
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: api.example.com
        port: http
        aliases:
          - www.example.com
`), "aliases[0]: conflicts with alias applications.api.routes.public.aliases[0]"},
		"bucket route": {aliasApp("          - files.example.com\n", `
buckets:
  files:
    route:
      domain: files.example.com
`), "buckets.files.route.domain: conflicts with alias applications.web.routes.public.aliases[0]"},
	} {
		_, err := compileAliasSource(t, test.source)
		require.ErrorContains(t, err, test.message, name)
	}
}

// TestResolveRouteAliases: aliases join the resolved hostname list after
// every route (so admission claims them), resolve and canonicalize like a
// route domain, and refuse after resolving a hostname that a route on any
// path, another alias, or a bucket route also resolves to, without
// naming the resolved value.
func TestResolveRouteAliases(t *testing.T) {
	t.Parallel()
	result, err := compileAliasSource(t, aliasApp("          - www.${APP_DOMAIN}\n          - ${LEGACY_DOMAIN}\n", `
  api:
    image: example.invalid/api:1
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: ${API_DOMAIN}
        path: /v1
        port: http
buckets:
  files:
    route:
      domain: ${STORAGE_DOMAIN}
`))
	require.NoError(t, err)

	routes, err := ResolveRoutes(result.Definition, map[string]string{
		"APP_DOMAIN": "example.com", "LEGACY_DOMAIN": "Old.Example.com",
		"API_DOMAIN": "api.example.com", "STORAGE_DOMAIN": "files.example.com",
	})
	require.NoError(t, err)
	require.Equal(t, []ResolvedRoute{
		{Application: "api", Key: "public", Domain: "api.example.com", Path: "/v1", Field: "applications.api.routes.public"},
		{Application: "web", Key: "public", Domain: "example.com", Path: "/", Field: "applications.web.routes.public"},
		{Application: "web", Key: "public", Alias: true, Domain: "www.example.com", Path: "/", Field: "applications.web.routes.public.aliases[0]"},
		{Application: "web", Key: "public", Alias: true, Domain: "old.example.com", Path: "/", Field: "applications.web.routes.public.aliases[1]"},
		{Bucket: "files", Key: "route", Domain: "files.example.com", Path: "/", Field: "buckets.files.route"},
	}, routes)

	for name, test := range map[string]struct {
		variables map[string]string
		field     string
		conflict  string
	}{
		"route on another path": {
			map[string]string{"APP_DOMAIN": "example.com", "LEGACY_DOMAIN": "shared.example.com", "API_DOMAIN": "shared.example.com", "STORAGE_DOMAIN": "files.example.com"},
			"applications.web.routes.public.aliases[1]", "applications.api.routes.public",
		},
		"another alias": {
			map[string]string{"APP_DOMAIN": "example.com", "LEGACY_DOMAIN": "WWW.example.com", "API_DOMAIN": "api.example.com", "STORAGE_DOMAIN": "files.example.com"},
			"applications.web.routes.public.aliases[1]", "applications.web.routes.public.aliases[0]",
		},
		"bucket route": {
			map[string]string{"APP_DOMAIN": "example.com", "LEGACY_DOMAIN": "shared.example.com", "API_DOMAIN": "api.example.com", "STORAGE_DOMAIN": "shared.example.com"},
			"buckets.files.route", "applications.web.routes.public.aliases[1]",
		},
	} {
		_, err := ResolveRoutes(result.Definition, test.variables)
		var routeErr *RouteError
		require.ErrorAs(t, err, &routeErr, name)
		require.Equal(t, test.field, routeErr.Field, name)
		require.Contains(t, routeErr.Detail, "conflicts with "+test.conflict, name)
		require.NotContains(t, routeErr.Detail, "shared.example.com", "resolved values stay out of errors")
	}

	_, err = ResolveRoutes(result.Definition, map[string]string{
		"APP_DOMAIN": "example.com", "LEGACY_DOMAIN": "bad host", "API_DOMAIN": "api.example.com", "STORAGE_DOMAIN": "files.example.com",
	})
	var routeErr *RouteError
	require.ErrorAs(t, err, &routeErr)
	require.Equal(t, "applications.web.routes.public.aliases[1]", routeErr.Field)
	require.Contains(t, routeErr.Detail, "resolved from ${LEGACY_DOMAIN}")
}
