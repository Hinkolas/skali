package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestScopedDatabaseSurvivesChildrenAndDropsWithParent(t *testing.T) {
	dsn := os.Getenv(envVar)
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL")
	}
	var name string
	t.Run("scope", func(scope *testing.T) {
		var pool *pgxpool.Pool
		scope.Run("setup", func(child *testing.T) {
			pool = NewScoped(child, scope)
			require.NoError(child, pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&name))
		})
		scope.Run("reuse", func(child *testing.T) {
			require.NotNil(child, pool)
			var one int
			require.NoError(child, pool.QueryRow(context.Background(), "SELECT 1").Scan(&one))
			require.Equal(child, 1, one)
		})
	})
	admin, err := pgx.Connect(context.Background(), dsn)
	require.NoError(t, err)
	defer admin.Close(context.Background())
	var exists bool
	require.NoError(t, admin.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)", name).Scan(&exists))
	require.False(t, exists, "the shared database must not outlive its parent suite")
}
