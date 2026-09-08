package compiler

import (
	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/stretchr/testify/require"
	"go/ast"
	"go/parser"
	"testing"
)

func TestRouteLiteralsCannotInjectRules(t *testing.T) {
	payload := "/`) || Host(`console.example.com`) && PathPrefix(`/auth"
	rule := edge.HostMatch("harmless.example.com", payload)
	expr, err := parser.ParseExpr(rule)
	require.NoError(t, err)
	calls := 0
	ast.Inspect(expr, func(n ast.Node) bool {
		if _, ok := n.(*ast.CallExpr); ok {
			calls++
		}
		return true
	})
	require.Equal(t, 2, calls, "payload must remain inside the PathPrefix literal")
	for _, host := range []string{"*.example.com", "a:443", "https://a.com", "a b", "a`)", "127.0.0.1", "é.com", "a..b"} {
		_, err := edge.CanonicalDomain(host)
		require.Error(t, err, host)
	}
	host, err := edge.CanonicalDomain("APP.Example.COM.")
	require.NoError(t, err)
	require.Equal(t, "app.example.com", host)
}

func TestResolvedDomainAliasesConflict(t *testing.T) {
	doc, err := manifest.Parse([]byte(`version: "1"
name: routes
applications:
  web:
    image: nginx:alpine
    routes:
      a: {domain: "${A}", port: 80}
      b: {domain: "${B}", port: 80}
`), "skali.yml")
	require.NoError(t, err)
	result, err := Compile(doc)
	require.NoError(t, err)
	_, err = ResolveRoutes(result.Definition, map[string]string{"A": "Example.COM.", "B": "example.com"})
	require.ErrorContains(t, err, "conflicts")
	_, err = ResolveRoutes(result.Definition, map[string]string{"A": "bad`host", "B": "example.com"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "bad`host")
}
