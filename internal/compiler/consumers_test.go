package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceConsumers(t *testing.T) {
	t.Parallel()
	output := func(collection, service, output string) Expression {
		return Expression{Parts: []ExpressionPart{{Kind: "service_output", Collection: collection, Service: service, Output: output}}}
	}
	definition := &ProjectDefinition{Applications: map[string]Application{
		"web": {Environment: map[string]Expression{
			"S3_ENDPOINT": output("buckets", "files", "endpoint"),
			"DB_URL":      output("databases", "data", "url"),
		}},
		"worker": {Environment: map[string]Expression{
			"S3_SECRET_KEY": {Parts: []ExpressionPart{
				{Kind: "literal", Value: "prefix-"},
				{Kind: "service_output", Collection: "buckets", Service: "files", Output: "secret_key", Sensitive: true},
			}},
		}},
		"admin": {Environment: map[string]Expression{
			"UPLOADS": output("buckets", "uploads", "name"),
			"NAME":    {Parts: []ExpressionPart{{Kind: "project_variable", Name: "NAME"}}},
		}},
		"static": {},
	}}

	require.Equal(t, []string{"web", "worker"}, ServiceConsumers(definition, "buckets", "files"))
	require.Equal(t, []string{"admin"}, ServiceConsumers(definition, "buckets", "uploads"))
	require.Equal(t, []string{"web"}, ServiceConsumers(definition, "databases", "data"))
	require.Empty(t, ServiceConsumers(definition, "buckets", "missing"))
	require.Empty(t, ServiceConsumers(nil, "buckets", "files"))
}
