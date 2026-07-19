package values

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
)

var requirements = []compiler.VariableRequirement{
	{Name: "APP_DOMAIN", Required: true},
	{Name: "SESSION_SECRET", Required: true, Secret: true},
	{Name: "LOG_LEVEL", Default: "info", HasDefault: true},
}

func TestParseSupportsCommentsAndQuotes(t *testing.T) {
	t.Parallel()
	file, err := Parse([]byte("# comment\nAPP_DOMAIN=files.localhost\nSESSION_SECRET=\"quoted value\"\n"), ".env")
	require.NoError(t, err)
	require.Equal(t, "files.localhost", file.Values["APP_DOMAIN"])
	require.Equal(t, "quoted value", file.Values["SESSION_SECRET"])
}

func TestResolveSeparatesSecrets(t *testing.T) {
	t.Parallel()
	file := &File{Values: map[string]string{
		"APP_DOMAIN":     "files.localhost",
		"SESSION_SECRET": "s3cret",
	}}
	resolved, err := Resolve(requirements, file, Options{})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"APP_DOMAIN": "files.localhost"}, resolved.Plain)
	require.Equal(t, map[string]string{"SESSION_SECRET": "s3cret"}, resolved.Secret)
	require.Equal(t, map[string]string{
		"APP_DOMAIN":     "files.localhost",
		"SESSION_SECRET": "s3cret",
	}, resolved.Merged())
}

func TestResolveReportsMissingAndUnknownTogether(t *testing.T) {
	t.Parallel()
	file := &File{Values: map[string]string{
		"TYPO": "value",
		// An empty value counts as unset, mirroring expression resolution.
		"APP_DOMAIN": "",
	}}
	_, err := Resolve(requirements, file, Options{})
	require.ErrorContains(t, err, "missing required project values: APP_DOMAIN, SESSION_SECRET")
	require.ErrorContains(t, err, "unknown project values: TYPO")
}

func TestResolveIgnoreUnknownDropsKeys(t *testing.T) {
	t.Parallel()
	file := &File{Values: map[string]string{
		"APP_DOMAIN":     "files.localhost",
		"SESSION_SECRET": "s3cret",
		"TYPO":           "value",
	}}
	resolved, err := Resolve(requirements, file, Options{IgnoreUnknown: true})
	require.NoError(t, err)
	require.NotContains(t, resolved.Merged(), "TYPO")
}

func TestResolveLeavesDefaultsToTheDefinition(t *testing.T) {
	t.Parallel()
	file := &File{Values: map[string]string{
		"APP_DOMAIN":     "files.localhost",
		"SESSION_SECRET": "s3cret",
	}}
	resolved, err := Resolve(requirements, file, Options{})
	require.NoError(t, err)
	// LOG_LEVEL is optional and absent: it is not materialized here, its
	// default applies during expression resolution.
	require.NotContains(t, resolved.Plain, "LOG_LEVEL")
}
