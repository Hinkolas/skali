package authz

import "slices"

// Protected: the environment's deploy policy is promote-only, so its running
// revision only changes through a promotion from an allowed source, a
// rollback, or an explicit recorded bypass. Values, restore, exec, and
// backups are not policy-gated; the policy is about untested code reaching
// the environment.
func (e *EnvironmentGrant) Protected() bool {
	return e.Settings.DeployPolicy == DeployPolicyPromoteOnly
}

// AcceptsPromotionFrom reports whether a promotion from the named source
// environment passes the policy: always when the environment is not
// protected; under promote-only when the allowed list is empty (any
// environment of the project) or names the source.
func (e *EnvironmentGrant) AcceptsPromotionFrom(source string) bool {
	if !e.Protected() {
		return true
	}
	return len(e.Settings.PromoteFrom) == 0 || slices.Contains(e.Settings.PromoteFrom, source)
}
