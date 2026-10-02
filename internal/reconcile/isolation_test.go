package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/store"
)

// A target that cannot render keeps the running environment isolated: the
// pass applies the namespace and the ingress policy before it records the
// render failure, and nothing else. The same failure on an environment
// that never activated applies nothing (a first deploy that fails to
// render creates no namespace).
func TestReconcileRenderFailureKeepsIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	policyOp := func(f *kernelFixture) string {
		return "apply NetworkPolicy/" + f.namespace + "/" + rendering.EnvironmentPolicyName
	}
	// An intercept naming a port the application does not have fails
	// intercept resolution inside desiredSet: a permanent render failure
	// for this target, not the host-gateway wait.
	breakRender := func(t *testing.T, f *kernelFixture) {
		require.NoError(t, f.st.InsertEnvironmentIntercept(ctx, store.InsertEnvironmentInterceptParams{
			EnvironmentID: f.environmentID, ApplicationKey: "web", Ports: []byte(`{"nope":20000}`),
		}))
	}

	t.Run("active", func(t *testing.T) {
		t.Parallel()
		f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
		f.deployedAndActive(t)
		breakRender(t, f)

		f.cluster.ops = nil
		requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
		require.NoError(t, err)
		require.Zero(t, requeue, "an unrenderable target waits for a new target")

		ops := f.cluster.recorded()
		require.Equal(t, []string{"apply Namespace//" + f.namespace, policyOp(f)}, ops,
			"only the namespace and its isolation are applied on a render failure")
		require.NotNil(t, f.cluster.lastApplied("NetworkPolicy/"+f.namespace+"/"+rendering.EnvironmentPolicyName))
	})

	t.Run("never active", func(t *testing.T) {
		t.Parallel()
		f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
		f.executeDeployment(t)
		f.fake.SetFresh()
		breakRender(t, f)

		requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
		require.NoError(t, err)
		require.Zero(t, requeue)
		require.Empty(t, f.cluster.recorded(), "nothing exists to isolate before the first revision renders")
	})
}
