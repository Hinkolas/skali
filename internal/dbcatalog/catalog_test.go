package dbcatalog

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalog(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{"postgres"}, Engines())
	require.Equal(t, []int{17, 18}, Majors("postgres"))
	require.Empty(t, Majors("mysql"))

	for _, major := range Majors("postgres") {
		require.True(t, Supports("postgres", major, "pg_trgm"), "major %d", major)
		require.True(t, Supports("postgres", major, "vector"),
			"the stock images package pgvector for major %d", major)
		require.False(t, Supports("postgres", major, "postgis"), "postgis waits for a blessed image")
		require.False(t, Supports("postgres", major, "pgaudit"), "preload-only extensions are not offered")
		extensions := Extensions("postgres", major)
		require.True(t, slices.IsSorted(extensions))
		require.Equal(t, slices.Compact(slices.Clone(extensions)), extensions, "no duplicates")
	}
	require.False(t, Supports("postgres", 12, "pg_trgm"))
	require.Nil(t, Extensions("postgres", 12))
	require.Nil(t, Extensions("mysql", 8))

	all := AllExtensions()
	require.True(t, slices.IsSorted(all))
	require.Contains(t, all, "vector")
	require.Equal(t, Extensions("postgres", 17), all, "one list serves both majors today")
}
