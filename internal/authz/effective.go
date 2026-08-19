package authz

// Effective computes a user's role on one environment. First match wins:
//
//  1. instance admin                        -> admin
//  2. a cell exists for (user, environment) -> the cell
//  3. project role is admin                 -> admin (project admins are never capped)
//  4. otherwise                             -> min(project role, environment max_role)
//
// A non-member has no project role; without a cell they get none. Cells are
// not capped: they are how exceptions above the ceiling are named.
func Effective(instanceAdmin, member bool, projectRole Role, cell *Role, maxRole Role) Role {
	if instanceAdmin {
		return Admin
	}
	if cell != nil {
		return *cell
	}
	if !member {
		return None
	}
	if projectRole == Admin {
		return Admin
	}
	if projectRole > maxRole {
		return maxRole
	}
	return projectRole
}
