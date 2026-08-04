package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/manifest"
)

func expressionProject() manifest.Project {
	return manifest.Project{
		Databases: map[string]manifest.Database{"data": {}},
		Buckets:   map[string]manifest.Bucket{"files": {}},
	}
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
		"bad key":               {"{{databases.Bad.url}}", "keys match ^[a-z][a-z0-9-]{0,62}$"},
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
