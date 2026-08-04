package values

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
)

var requirements = []compiler.VariableRequirement{
	{Name: "APP_DOMAIN", Required: true},
	{Name: "SESSION_SECRET", Required: true},
	{Name: "LOG_LEVEL", Default: "info", HasDefault: true},
}

func TestParseSupportsCommentsAndQuotes(t *testing.T) {
	t.Parallel()
	file, err := Parse([]byte("# comment\nAPP_DOMAIN=files.localhost\nSESSION_SECRET=\"quoted value\"\n"), ".env")
	require.NoError(t, err)
	require.Equal(t, "files.localhost", file.Values["APP_DOMAIN"])
	require.Equal(t, "quoted value", file.Values["SESSION_SECRET"])
}

func TestConformIntersects(t *testing.T) {
	t.Parallel()
	kept, missing, orphaned := Conform(requirements, map[string]string{
		"APP_DOMAIN":     "files.localhost",
		"SESSION_SECRET": "s3cret",
		"REMOVED":        "stale",
	})
	require.Equal(t, map[string]string{
		"APP_DOMAIN":     "files.localhost",
		"SESSION_SECRET": "s3cret",
	}, kept)
	require.Empty(t, missing)
	require.Equal(t, []string{"REMOVED"}, orphaned)
}

func TestConformReportsMissingRequired(t *testing.T) {
	t.Parallel()
	_, missing, _ := Conform(requirements, map[string]string{"APP_DOMAIN": "files.localhost"})
	require.Equal(t, []string{"SESSION_SECRET"}, missing)
}

// A present empty string is a real value, never "unset".
func TestConformTreatsEmptyStringAsPresent(t *testing.T) {
	t.Parallel()
	kept, missing, _ := Conform(requirements, map[string]string{
		"APP_DOMAIN":     "",
		"SESSION_SECRET": "s3cret",
	})
	require.Empty(t, missing)
	require.Equal(t, "", kept["APP_DOMAIN"])
}

func TestConformLeavesDefaultsToTheDefinition(t *testing.T) {
	t.Parallel()
	kept, missing, _ := Conform(requirements, map[string]string{
		"APP_DOMAIN":     "files.localhost",
		"SESSION_SECRET": "s3cret",
	})
	require.Empty(t, missing)
	// LOG_LEVEL is optional and absent: it is not materialized here, its
	// default applies during expression resolution.
	require.NotContains(t, kept, "LOG_LEVEL")
}

// Conform is generic so the same contract check serves dotenv strings and
// stored version references.
func TestConformOverVersionRefs(t *testing.T) {
	t.Parallel()
	kept, missing, orphaned := Conform(requirements, map[string]int{
		"APP_DOMAIN":     3,
		"SESSION_SECRET": 1,
		"REMOVED":        7,
	})
	require.Equal(t, map[string]int{"APP_DOMAIN": 3, "SESSION_SECRET": 1}, kept)
	require.Empty(t, missing)
	require.Equal(t, []string{"REMOVED"}, orphaned)
}
