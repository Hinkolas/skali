package cnpg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestLoginRoleName(t *testing.T) {
	t.Parallel()
	require.Equal(t, "u_data_abcd1234_v2", LoginRoleName("u_data_abcd1234", 2))
	// The longest owner the substrate generates ("u_" + 24 + "_" + 8) stays
	// well inside PostgreSQL's identifier limit with a version suffix.
	owner := "u_" + strings.Repeat("x", 24) + "_abcd1234"
	require.True(t, ValidRoleName(LoginRoleName(owner, 1000000)))
	require.False(t, ValidRoleName(strings.Repeat("a", MaxRoleNameLength+1)))
	require.False(t, ValidRoleName("u_data;drop"))
	require.False(t, ValidRoleName("U_data"))
	require.False(t, ValidRoleName(""))
}

func TestPSQLCommand(t *testing.T) {
	t.Parallel()
	command := PSQLCommand("db_data", "SELECT 1;")
	require.Equal(t, "psql", command[0])
	require.Contains(t, command, "ON_ERROR_STOP=1")
	require.Equal(t, "SELECT 1;", command[len(command)-1], "the script is one argv entry, never a shell string")
	require.Equal(t, "-d", command[len(command)-4])
	require.Equal(t, "db_data", command[len(command)-3])
	require.Equal(t, "cnpg.io/cluster=pg17-shared,cnpg.io/instanceRole=primary", PrimarySelector("pg17-shared"))
	query := PSQLQuery("postgres", RoleLoginQuery("u_data"))
	require.Contains(t, query, "-t")
	require.Contains(t, query, "-A")
	require.Equal(t, "SELECT rolcanlogin FROM pg_catalog.pg_roles WHERE rolname = 'u_data'", query[len(query)-1])
	require.Equal(t, "SELECT count(*) FROM pg_catalog.pg_roles WHERE rolname IN ('u_data_v2', 'u_data')",
		RolesPresentQuery([]string{"u_data_v2", "u_data"}))
}

func TestRoleScripts(t *testing.T) {
	t.Parallel()
	take := TakeLoginRoleSQL("u_data_v2", "u_data")
	require.Contains(t, take, `rolname = 'u_data_v2'`, "every script runs only while its role exists")
	require.Contains(t, take, `ALTER ROLE "u_data_v2" SET role = ''u_data''`, "the login role acts as the owner from login on")

	owner := RetireOwnerLoginSQL("u_data")
	require.Contains(t, owner, `ALTER ROLE "u_data" NOLOGIN`)
	require.Contains(t, owner, `pg_terminate_backend(pid)`)
	require.Contains(t, owner, `usename = 'u_data' AND pid <> pg_catalog.pg_backend_pid()`)
	require.NotContains(t, owner, "DROP", "the owner keeps the database and its objects")

	login := RetireLoginRoleSQL("u_data_v2", "u_data")
	for _, want := range []string{
		`ALTER ROLE "u_data_v2" NOLOGIN`,
		`usename = 'u_data_v2'`,
		`REASSIGN OWNED BY "u_data_v2" TO "u_data"`,
		`DROP OWNED BY "u_data_v2"`,
		`DROP ROLE "u_data_v2"`,
	} {
		require.Contains(t, login, want)
	}
	first := strings.Index(login, "NOLOGIN")
	require.Less(t, first, strings.Index(login, "pg_terminate_backend"), "no new session may slip in after the terminate")
	require.Less(t, strings.Index(login, "REASSIGN"), strings.Index(login, "DROP ROLE"))

	terminate := TerminateSessionsSQL([]string{"u_data_v3", "u_data"})
	require.Contains(t, terminate, "usename IN ('u_data_v3', 'u_data')")
	require.NotContains(t, terminate, "DROP")

	drop := DropTenantRolesSQL([]string{"u_data_v3", "u_data"})
	require.Less(t, strings.Index(drop, `DROP ROLE "u_data_v3"`), strings.Index(drop, `DROP ROLE "u_data"`),
		"login roles go before the owner they are members of")
	require.NotContains(t, drop, "REASSIGN", "nothing is left to hand over once the database is gone")

	// Quoting survives a hostile name even though the substrate validates
	// names before they reach a script.
	require.Contains(t, TakeLoginRoleSQL(`a"b`, `c'd`), `ALTER ROLE "a""b" SET role = ''c''''d''`)
}

func clusterWithRoles(reconciled []any, passwords map[string]any, failing map[string]any) *unstructured.Unstructured {
	status := map[string]any{
		"byStatus": map[string]any{
			"reconciled":  reconciled,
			"not-managed": []any{"postgres", "u_other"},
		},
	}
	if passwords != nil {
		status["passwordStatus"] = passwords
	}
	if failing != nil {
		status["cannotReconcile"] = failing
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"managedRolesStatus": status},
	}}
}

func TestRoleReconciled(t *testing.T) {
	t.Parallel()
	cluster := clusterWithRoles([]any{"u_data_v2"}, map[string]any{
		"u_data_v2": map[string]any{"resourceVersion": "42", "transactionID": int64(7)},
	}, nil)
	ready, reason := RoleReconciled(cluster, "u_data_v2", "42")
	require.True(t, ready, reason)

	ready, reason = RoleReconciled(cluster, "u_data_v2", "43")
	require.False(t, ready)
	require.Contains(t, reason, "apply its password")

	ready, reason = RoleReconciled(cluster, "u_data_v3", "1")
	require.False(t, ready)
	require.Contains(t, reason, "create it")

	failing := clusterWithRoles(nil, nil, map[string]any{"u_data_v3": []any{"the referenced password Secret cannot be fetched"}})
	ready, reason = RoleReconciled(failing, "u_data_v3", "1")
	require.False(t, ready)
	require.Contains(t, reason, "Secret cannot be fetched")

	ready, _ = RoleReconciled(&unstructured.Unstructured{Object: map[string]any{}}, "u_data_v2", "1")
	require.False(t, ready, "a pool without role status has not created anything")
}
