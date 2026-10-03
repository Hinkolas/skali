package cnpg

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Credential rotation keeps a tenant's ownership on one role that never
// logs in and alternates login roles under it. CNPG creates, grants and
// passwords the login roles from the Cluster spec; the one setting it
// cannot express, the login role acting as the owner, and the retirement
// of a role it no longer lists run as `psql` in the pool's primary over the
// instance's local socket (peer authentication maps the pod's user to
// postgres, which is how `kubectl cnpg psql` works). Every script here is
// idempotent, so a pass that crashed halfway is simply re-run.

// PostgresContainer is the instance pod's PostgreSQL container.
const PostgresContainer = "postgres"

// MaxRoleNameLength is PostgreSQL's identifier limit (NAMEDATALEN - 1).
const MaxRoleNameLength = 63

// PrimarySelector selects a pool's primary instance pod.
func PrimarySelector(pool string) string {
	return LabelCluster + "=" + pool + "," + LabelInstanceRole + "=" + RolePrimary
}

// PSQLCommand is the argv running one script as the postgres superuser in
// the given database: no shell, errors stop the script, and the whole
// string runs in one transaction (psql -c semantics).
func PSQLCommand(database, script string) []string {
	return []string{"psql", "-v", "ON_ERROR_STOP=1", "-X", "-q", "-U", "postgres", "-d", database, "-c", script}
}

// PSQLQuery is the argv running one query whose bare result (no header,
// no alignment) comes back on stdout.
func PSQLQuery(database, query string) []string {
	return []string{"psql", "-v", "ON_ERROR_STOP=1", "-X", "-q", "-t", "-A", "-U", "postgres", "-d", database, "-c", query}
}

// RoleLoginQuery answers "t" or "f" for the role's LOGIN attribute, and
// nothing when the role does not exist.
func RoleLoginQuery(role string) string {
	return "SELECT rolcanlogin FROM pg_catalog.pg_roles WHERE rolname = " + quoteLiteral(role)
}

// RolesPresentQuery counts how many of the roles exist.
func RolesPresentQuery(roles []string) string {
	literals := make([]string, 0, len(roles))
	for _, role := range roles {
		literals = append(literals, quoteLiteral(role))
	}
	return "SELECT count(*) FROM pg_catalog.pg_roles WHERE rolname IN (" + strings.Join(literals, ", ") + ")"
}

// LoginRoleName is the login role a tenant takes at the given credential
// version: the owner role with a version suffix.
func LoginRoleName(owner string, version int64) string {
	return fmt.Sprintf("%s_v%d", owner, version)
}

var sqlIdentifier = regexp.MustCompile(`^[a-z0-9_]+$`)

// ValidRoleName reports whether a generated role name is safe to embed in
// a script: lowercase, digits and underscores only, within the identifier
// limit. The scripts quote names regardless; this is the second lock.
func ValidRoleName(name string) bool {
	return name != "" && len(name) <= MaxRoleNameLength && sqlIdentifier.MatchString(name)
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// roleExists wraps statements in a DO block that runs them only while the
// role exists, which makes every script re-runnable after a crash.
func roleExists(role string, body ...string) string {
	return "DO $$ BEGIN IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = " + quoteLiteral(role) + ") THEN " +
		strings.Join(body, " ") + " END IF; END $$;"
}

func execute(statement string) string {
	return "EXECUTE " + quoteLiteral(statement) + ";"
}

// terminateSessions ends every session the role opened, except the one
// running the script. Sessions of other login roles acting as this role
// through SET role are not its sessions and stay.
func terminateSessions(role string) string {
	return "PERFORM pg_catalog.pg_terminate_backend(pid) FROM pg_catalog.pg_stat_activity " +
		"WHERE usename = " + quoteLiteral(role) + " AND pid <> pg_catalog.pg_backend_pid();"
}

// TakeLoginRoleSQL makes every new session of the login role act as the
// owner, so objects the application creates belong to the owner and the
// login role can later be dropped without reassigning anything. Run in
// any database; the setting is cluster-wide.
func TakeLoginRoleSQL(login, owner string) string {
	return roleExists(login,
		execute("ALTER ROLE "+quoteIdentifier(login)+" SET role = "+quoteLiteral(owner)))
}

// RetireOwnerLoginSQL closes the owner role's own login after the first
// rotation: no new sessions, open ones terminated. The role keeps its
// ownership; CNPG wipes the dormant password from the spec.
func RetireOwnerLoginSQL(owner string) string {
	return roleExists(owner,
		execute("ALTER ROLE "+quoteIdentifier(owner)+" NOLOGIN"),
		terminateSessions(owner))
}

// RetireLoginRoleSQL drops a login role once its window has passed: no
// new sessions, open ones terminated, anything it owns handed to the
// owner (it owns nothing when its sessions acted as the owner; the
// reassignment covers an application that reset its role), then the role
// goes. Run in the tenant's database, where its objects live.
func RetireLoginRoleSQL(login, owner string) string {
	return roleExists(login,
		execute("ALTER ROLE "+quoteIdentifier(login)+" NOLOGIN"),
		terminateSessions(login),
		execute("REASSIGN OWNED BY "+quoteIdentifier(login)+" TO "+quoteIdentifier(owner)),
		execute("DROP OWNED BY "+quoteIdentifier(login)),
		execute("DROP ROLE "+quoteIdentifier(login)))
}

// DropTenantRolesSQL removes every role of a released tenant after its
// database is gone: login roles first, the owner last. Run in the
// postgres database; DROP OWNED there releases what the roles still hold
// on shared objects.
func DropTenantRolesSQL(roles []string) string {
	scripts := make([]string, 0, len(roles))
	for _, role := range roles {
		scripts = append(scripts, roleExists(role,
			execute("ALTER ROLE "+quoteIdentifier(role)+" NOLOGIN"),
			terminateSessions(role),
			execute("DROP OWNED BY "+quoteIdentifier(role)),
			execute("DROP ROLE "+quoteIdentifier(role))))
	}
	return strings.Join(scripts, " ")
}

// RoleReconciled reports whether CNPG has the role in place with the
// password from the Secret at the given resource version
// (status.managedRolesStatus). The reason explains a false answer.
func RoleReconciled(cluster *unstructured.Unstructured, role, secretVersion string) (bool, string) {
	reconciled, _, _ := unstructured.NestedStringSlice(cluster.Object, "status", "managedRolesStatus", "byStatus", "reconciled")
	found := false
	for _, name := range reconciled {
		if name == role {
			found = true
			break
		}
	}
	if !found {
		if failures, _, _ := unstructured.NestedStringSlice(cluster.Object, "status", "managedRolesStatus", "cannotReconcile", role); len(failures) > 0 {
			return false, fmt.Sprintf("role %s: %s", role, failures[0])
		}
		return false, fmt.Sprintf("role %s: waiting for the pool to create it", role)
	}
	applied, _, _ := unstructured.NestedString(cluster.Object, "status", "managedRolesStatus", "passwordStatus", role, "resourceVersion")
	if applied != secretVersion {
		return false, fmt.Sprintf("role %s: waiting for the pool to apply its password", role)
	}
	return true, ""
}
