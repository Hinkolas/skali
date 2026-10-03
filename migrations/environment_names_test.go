package migrations_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/migrations"
)

func TestEnvironmentNamesUpgradePreservesBackupPaths(t *testing.T) {
	db := database(t)
	ctx := t.Context()
	_, err := provider(t, db).DownTo(ctx, 11)
	require.NoError(t, err)
	project, environment, backup := uuid.New(), uuid.New(), uuid.New()
	_, err = db.ExecContext(ctx, "INSERT INTO projects (id,name) VALUES ($1,'rename-upgrade')", project)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "INSERT INTO environments (id,project_id,name) VALUES ($1,$2,'production')", environment, project)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "INSERT INTO backups (id,kind,environment_id,project_name,environment_name) VALUES ($1,'backup',$2,'rename-upgrade','production')", backup, environment)
	require.NoError(t, err)
	_, err = migrations.Up(ctx, db)
	require.NoError(t, err)
	var namespace string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT backup_namespace FROM environments WHERE id=$1", environment).Scan(&namespace))
	require.Equal(t, "production", namespace)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT environment_namespace FROM backups WHERE id=$1", backup).Scan(&namespace))
	require.Equal(t, "production", namespace)
}
