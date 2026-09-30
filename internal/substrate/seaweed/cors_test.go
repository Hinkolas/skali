package seaweed

import (
	"testing"

	"github.com/minio/minio-go/v7/pkg/cors"
	"github.com/stretchr/testify/require"
)

// TestCORSConfigAndComparison: the stored spec becomes one rule, nothing
// stored means no configuration, and the comparison treats an absent and
// an empty configuration alike while any field difference is drift.
func TestCORSConfigAndComparison(t *testing.T) {
	t.Parallel()
	none, err := CORSConfig(nil)
	require.NoError(t, err)
	require.Nil(t, none)
	require.True(t, sameCORS(nil, nil))
	require.True(t, sameCORS(cors.NewConfig(nil), nil))

	declared, err := CORSConfig([]byte(`{"allowedOrigins":["https://app.example"],"allowedMethods":["GET","PUT"],` +
		`"allowedHeaders":["content-type"],"exposeHeaders":["ETag"],"maxAgeSeconds":600}`))
	require.NoError(t, err)
	require.Len(t, declared.CORSRules, 1)
	rule := declared.CORSRules[0]
	require.Equal(t, []string{"https://app.example"}, rule.AllowedOrigin)
	require.Equal(t, []string{"GET", "PUT"}, rule.AllowedMethod)
	require.Equal(t, []string{"content-type"}, rule.AllowedHeader)
	require.Equal(t, []string{"ETag"}, rule.ExposeHeader)
	require.Equal(t, 600, rule.MaxAgeSeconds)

	require.False(t, sameCORS(nil, declared), "nothing on the bucket is drift from a declared policy")
	require.False(t, sameCORS(declared, nil), "a policy on the bucket is drift from none declared")
	same, err := CORSConfig([]byte(`{"allowedOrigins":["https://app.example"],"allowedMethods":["GET","PUT"],` +
		`"allowedHeaders":["content-type"],"exposeHeaders":["ETag"],"maxAgeSeconds":600}`))
	require.NoError(t, err)
	require.True(t, sameCORS(declared, same))
	changed := cors.NewConfig([]cors.Rule{{AllowedOrigin: []string{"https://other.example"}, AllowedMethod: []string{"GET", "PUT"},
		AllowedHeader: []string{"content-type"}, ExposeHeader: []string{"ETag"}, MaxAgeSeconds: 600}})
	require.False(t, sameCORS(declared, changed))

	_, err = CORSConfig([]byte(`{`))
	require.Error(t, err)
}
