package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/manifest"
)

func compileBucketSource(t *testing.T, cors string) (*Result, error) {
	t.Helper()
	source := "name: cors\nbuckets:\n  files:\n    quotas:\n      storage: 1GB\n" + cors
	document, err := manifest.Parse([]byte(source), "skali.yml")
	require.NoError(t, err)
	return Compile(document)
}

// TestCompileBucketCORS: a declared policy compiles to one rule with the
// S3 method set defaulted, and the mistakes a browser would silently
// suffer from (no origin, an origin with a path, an unknown method) are
// diagnostics at compile time.
func TestCompileBucketCORS(t *testing.T) {
	t.Parallel()
	none, err := compileBucketSource(t, "")
	require.NoError(t, err)
	require.Nil(t, none.Definition.Buckets["files"].CORS, "nothing declared keeps the store's fallback")

	full, err := compileBucketSource(t, `    cors:
      allowedOrigins: [https://app.example.com, "*"]
      allowedMethods: [get, PUT]
      allowedHeaders: [content-type]
      exposeHeaders: [ETag]
      maxAge: 10m
`)
	require.NoError(t, err)
	cors := full.Definition.Buckets["files"].CORS
	require.NotNil(t, cors)
	require.Equal(t, []string{"https://app.example.com", "*"}, cors.AllowedOrigins)
	require.Equal(t, []string{"GET", "PUT"}, cors.AllowedMethods, "methods are upper-cased")
	require.Equal(t, []string{"content-type"}, cors.AllowedHeaders)
	require.Equal(t, []string{"ETag"}, cors.ExposeHeaders)
	require.EqualValues(t, 600, cors.MaxAgeSeconds)

	defaulted, err := compileBucketSource(t, "    cors:\n      allowedOrigins: [https://app.example.com]\n")
	require.NoError(t, err)
	require.Equal(t, []string{"GET", "PUT"}, defaulted.Definition.Buckets["files"].CORS.AllowedMethods,
		"a download and a presigned upload when no methods are declared")

	_, err = compileBucketSource(t, "    cors:\n      allowedMethods: [GET]\n")
	require.ErrorContains(t, err, "buckets.files.cors.allowedOrigins: must list at least one origin")
	_, err = compileBucketSource(t, "    cors:\n      allowedOrigins: [https://app.example.com/upload]\n")
	require.ErrorContains(t, err, "buckets.files.cors.allowedOrigins[0]: must be an origin")
	_, err = compileBucketSource(t, "    cors:\n      allowedOrigins: [app.example.com]\n")
	require.ErrorContains(t, err, "buckets.files.cors.allowedOrigins[0]: must be an origin")
	_, err = compileBucketSource(t, "    cors:\n      allowedOrigins: [https://app.example.com]\n      allowedMethods: [PATCH]\n")
	require.ErrorContains(t, err, "buckets.files.cors.allowedMethods[0]: must be one of GET, PUT, POST, DELETE, HEAD")
}
