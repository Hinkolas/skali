// Package authz resolves who may do what on projects and environments. The
// model (docs/permissions.md) is one ladder of roles used at both levels:
// a project membership gives the member's default role on every environment
// of the project, per-environment cells override it for one user, and an
// environment ceiling caps what members inherit. Roles are code: the store
// records which role a user holds, this package decides what it means.
package authz

import (
	"fmt"
	"strings"
)

// Role is one step of the ladder none < read < deploy < maintain < admin.
// Each step includes everything below it.
type Role int

const (
	// None locks an environment: it stays listed by name, its contents
	// answer forbidden. Never a project role (a non-member has no row).
	None Role = iota
	// Read sees everything: status, deployments, runs and their logs,
	// runtime logs, value names, routes, connection info, backups, settings.
	Read
	// Deploy changes what code runs without touching the definition or the
	// configuration: promote, rollback, restart, cancel, backup create,
	// direct deploys whose definition is unchanged.
	Deploy
	// Maintain owns the full blast radius of skali.yml: definition changes,
	// values, restore, and the secret-bearing reads (exec, resolved
	// environment, credential reveal).
	Maintain
	// Admin manages what is outside the yaml: settings, other users' roles,
	// delete and teardown, protection bypass.
	Admin
)

var roleNames = [...]string{"none", "read", "deploy", "maintain", "admin"}

func (r Role) String() string {
	if r < None || r > Admin {
		return fmt.Sprintf("role(%d)", int(r))
	}
	return roleNames[r]
}

// AtLeast reports whether r grants everything min grants.
func (r Role) AtLeast(min Role) bool { return r >= min }

// ParseRole reads a role name as stored or sent by clients.
func ParseRole(s string) (Role, bool) {
	for i, name := range roleNames {
		if name == s {
			return Role(i), true
		}
	}
	return None, false
}

// ValidProjectRole accepts the roles a membership may hold: none is not a
// project role, a non-member simply has no row.
func ValidProjectRole(s string) bool {
	r, ok := ParseRole(s)
	return ok && r >= Read
}

// ValidCellRole accepts every role including none for per-environment cells.
func ValidCellRole(s string) bool {
	_, ok := ParseRole(s)
	return ok
}

// ValidMaxRole accepts every role including none for the environment ceiling.
func ValidMaxRole(s string) bool {
	_, ok := ParseRole(s)
	return ok
}

// Environment settings vocabulary, stored as text and validated here so the
// project service and the API share one definition.
const (
	DeployPolicyDirect      = "direct"
	DeployPolicyPromoteOnly = "promote-only"

	PriorityNormal = "normal"
	PriorityHigh   = "high"
)

func ValidDeployPolicy(s string) bool {
	return s == DeployPolicyDirect || s == DeployPolicyPromoteOnly
}

func ValidPriority(s string) bool {
	return s == PriorityNormal || s == PriorityHigh
}

// RoleNames lists the ladder for messages and schemas, lowest first.
func RoleNames() []string {
	names := make([]string, len(roleNames))
	copy(names, roleNames[:])
	return names
}

// ProjectRoleNames lists the roles a membership may hold.
func ProjectRoleNames() string {
	return strings.Join(roleNames[Read:], ", ")
}
