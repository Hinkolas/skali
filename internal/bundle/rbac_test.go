package bundle

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// A replicated store renders disruption budgets before it provisions the
// platform identity. Missing RBAC here leaves healthy storage unable to
// finish reconciliation, permanently blocking bucket backups and restores.
func TestSkalidCanManageObjectStoreDisruptionBudgets(t *testing.T) {
	t.Parallel()
	for name, profile := range map[string]Profile{"local": localProfile(), "production": productionProfile()} {
		t.Run(name, func(t *testing.T) {
			objects, err := Render(profile)
			require.NoError(t, err)
			var role rbacv1.ClusterRole
			for _, object := range objects.Skalid {
				if object.GetKind() == "ClusterRole" && object.GetName() == "skalid" {
					require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &role))
				}
			}
			require.NotEmpty(t, role.Rules)
			for _, name := range []string{"seaweed-master", "seaweed-filer"} {
				// Apply reads and patches (including initial creation);
				// teardown deletes the budgets.
				for _, verb := range []string{"get", "create", "patch", "delete"} {
					allowed := slices.ContainsFunc(role.Rules, func(rule rbacv1.PolicyRule) bool {
						return (slices.Contains(rule.APIGroups, "policy") || slices.Contains(rule.APIGroups, "*")) &&
							(slices.Contains(rule.Resources, "poddisruptionbudgets") || slices.Contains(rule.Resources, "*")) &&
							(slices.Contains(rule.Verbs, verb) || slices.Contains(rule.Verbs, "*")) &&
							(len(rule.ResourceNames) == 0 || (verb != "create" && slices.Contains(rule.ResourceNames, name)))
					})
					require.True(t, allowed, "skalid must be able to %s poddisruptionbudgets/%s", verb, name)
				}
			}
		})
	}
}
