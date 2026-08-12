package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/naming"
)

func expressionProject() manifest.Project {
	return manifest.Project{
		Databases: map[string]manifest.Database{"data": {}},
		Buckets:   map[string]manifest.Bucket{"files": {}},
	}
}

func TestExpandVariables(t *testing.T) {
	t.Parallel()
	lookup := func(name string) (string, bool) {
		values := map[string]string{"PORT": "20417", "SKALI_PORT_WEB": "20417"}
		value, ok := values[name]
		return value, ok
	}

	for raw, expected := range map[string]string{
		"":                        "",
		"plain":                   "plain",
		"--port=${PORT}":          "--port=20417",
		"${PORT}":                 "20417",
		"${MISSING:-fallback}":    "fallback",
		"${PORT:-5173}":           "20417",
		"$HOME and {x} and $":     "$HOME and {x} and $",
		"--format {{.ID}}":        "--format {{.ID}}",
		"stray }} passes through": "stray }} passes through",
	} {
		expanded, err := ExpandVariables(raw, lookup)
		require.NoError(t, err, raw)
		require.Equal(t, expected, expanded, raw)
	}

	_, err := ExpandVariables("${MISSING}", lookup)
	require.ErrorContains(t, err, "unknown variable ${MISSING}")

	_, err = ExpandVariables("${PORT", lookup)
	require.ErrorContains(t, err, "unterminated")
}

// Text without well-formed tokens passes through untouched: lone $, single
// braces, and shell syntax are literal, and the empty string is one empty
// literal part (the shape the canonical hashes depend on).
func TestParseExpressionLiteralPassthrough(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "plain", "$HOME and {x} and } and $ alone", "a{b}c"} {
		expression, err := parseExpression(raw, manifest.Project{}, true)
		require.NoError(t, err, raw)
		require.Equal(t, []ExpressionPart{{Kind: "literal", Value: raw}}, expression.Parts, raw)
	}
}

func TestParseExpressionTokenSequence(t *testing.T) {
	t.Parallel()
	expression, err := parseExpression("a${ONE}${TWO:-}b{{ databases.data.url }}c", expressionProject(), true)
	require.NoError(t, err)
	require.Equal(t, []ExpressionPart{
		{Kind: "literal", Value: "a"},
		{Kind: "project_variable", Name: "ONE"},
		{Kind: "project_variable", Name: "TWO", HasDefault: true},
		{Kind: "literal", Value: "b"},
		{Kind: "service_output", Collection: "databases", Service: "data", Output: "url", Sensitive: true},
		{Kind: "literal", Value: "c"},
	}, expression.Parts)
}

func TestParseExpressionDefaults(t *testing.T) {
	t.Parallel()
	expression, err := parseExpression("${A:-a:-b}", manifest.Project{}, false)
	require.NoError(t, err)
	require.Equal(t, []ExpressionPart{
		{Kind: "project_variable", Name: "A", Default: "a:-b", HasDefault: true},
	}, expression.Parts)

	// The default runs to the first closing brace; the rest is literal.
	expression, err = parseExpression("${A:-x}}", manifest.Project{}, false)
	require.NoError(t, err)
	require.Equal(t, []ExpressionPart{
		{Kind: "project_variable", Name: "A", Default: "x", HasDefault: true},
		{Kind: "literal", Value: "}"},
	}, expression.Parts)
}

func TestParseExpressionBucketOutput(t *testing.T) {
	t.Parallel()
	expression, err := parseExpression("{{buckets.files.endpoint}}", expressionProject(), true)
	require.NoError(t, err)
	require.Equal(t, []ExpressionPart{
		{Kind: "service_output", Collection: "buckets", Service: "files", Output: "endpoint"},
	}, expression.Parts)
}

func TestParseExpressionErrors(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		raw     string
		message string
	}{
		"unterminated variable": {"x${A", "unterminated ${...} expression at position 2"},
		"unterminated marker":   {"${", "unterminated ${...} expression at position 1"},
		"unterminated default":  {"${A:-x", "unterminated ${...} expression at position 1"},
		"lowercase name":        {"${lower}", "project value names match ^[A-Z_][A-Z0-9_]*$"},
		"empty name":            {"${}", "project value names match"},
		"bad separator":         {"${A:x}", "expected } or :- after the name"},
		"stray closing":         {"x}}y", "stray \"}}\" at position 2 has no matching \"{{\""},
		"unknown collection":    {"{{volumes.data.path}}", "the collection must be databases or buckets"},
		"missing dots":          {"{{databases}}", "expected {{collection.key.output}}"},
		"bad key":               {"{{databases.Bad.url}}", "keys match " + naming.KeyPattern},
		"bad output":            {"{{databases.data.URL}}", "outputs match ^[a-z_][a-z0-9_]*$"},
		"single closing brace":  {"{{databases.data.url}", "expected }}"},
		"unterminated output":   {"{{databases.data.url", "unterminated {{...}} expression at position 1"},
		"unknown output":        {"{{databases.data.hostname}}", "unknown database output \"hostname\""},
		"undeclared database":   {"{{databases.other.url}}", "references unknown database \"other\""},
		"undeclared bucket":     {"{{buckets.other.endpoint}}", "references unknown bucket \"other\""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := parseExpression(test.raw, expressionProject(), true)
			require.ErrorContains(t, err, test.message)
		})
	}
}

// Outputs are rejected where the position disallows them even when the
// token itself is well-formed.
func TestParseExpressionOutputsNotAllowed(t *testing.T) {
	t.Parallel()
	_, err := parseExpression("{{databases.data.url}}", expressionProject(), false)
	require.ErrorContains(t, err, "service-output references are not allowed here")
}

func TestResolveExpressionOutputs(t *testing.T) {
	t.Parallel()
	outputs := map[string]map[string]string{
		"databases.data": {"url": "postgresql://u:p@127.0.0.1:30501/db", "host": "127.0.0.1"},
		"buckets.files":  {"endpoint": "http://127.0.0.1:30510"},
	}

	parse := func(raw string) Expression {
		expression, err := parseExpression(raw, expressionProject(), true)
		require.NoError(t, err)
		return expression
	}

	// Pure literal and project variables behave exactly like ResolveExpression.
	resolved, err := ResolveExpressionOutputs(parse("plain"), nil, outputs)
	require.NoError(t, err)
	require.Equal(t, "plain", resolved)
	resolved, err = ResolveExpressionOutputs(parse("${NAME:-fallback}"), nil, outputs)
	require.NoError(t, err)
	require.Equal(t, "fallback", resolved)
	resolved, err = ResolveExpressionOutputs(parse("${NAME}"), map[string]string{"NAME": "value"}, outputs)
	require.NoError(t, err)
	require.Equal(t, "value", resolved)

	// Single output and a composed literal+variable+output expression.
	resolved, err = ResolveExpressionOutputs(parse("{{databases.data.url}}"), nil, outputs)
	require.NoError(t, err)
	require.Equal(t, "postgresql://u:p@127.0.0.1:30501/db", resolved)
	resolved, err = ResolveExpressionOutputs(
		parse("s3://${BUCKET_PREFIX:-x}@{{ buckets.files.endpoint }}/path"), nil, outputs)
	require.NoError(t, err)
	require.Equal(t, "s3://x@http://127.0.0.1:30510/path", resolved)

	// Missing variable, missing output set, and missing output name all fail.
	_, err = ResolveExpressionOutputs(parse("${NAME}"), nil, outputs)
	require.ErrorContains(t, err, "missing project variable NAME")
	_, err = ResolveExpressionOutputs(parse("{{databases.data.url}}"), nil, nil)
	require.ErrorContains(t, err, "missing outputs for databases.data")
	_, err = ResolveExpressionOutputs(parse("{{databases.data.port}}"), nil, outputs)
	require.ErrorContains(t, err, "missing output databases.data.port")
}

func TestEndpointBearingOutput(t *testing.T) {
	t.Parallel()
	require.True(t, EndpointBearingOutput("databases", "host"))
	require.True(t, EndpointBearingOutput("databases", "port"))
	require.True(t, EndpointBearingOutput("databases", "url"))
	require.True(t, EndpointBearingOutput("buckets", "endpoint"))
	require.False(t, EndpointBearingOutput("databases", "password"))
	require.False(t, EndpointBearingOutput("buckets", "access_key"))
	require.False(t, EndpointBearingOutput("volumes", "size"))

	// Every endpoint-bearing entry must exist in the catalog so the
	// classification cannot drift from the outputs that actually render.
	for collection, outputs := range endpointBearing {
		for output := range outputs {
			_, ok := outputCatalog[collection][output]
			require.True(t, ok, "%s.%s not in outputCatalog", collection, output)
		}
	}
}
