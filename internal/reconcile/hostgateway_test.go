package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/deploy"
)

// An unresolvable host gateway is transient on the local platform: the
// render step waits visibly and the run stays running; once the name
// resolves the wait closes and the rollout proceeds to activation.
func TestReconcileHostGatewayUnresolvedWaits(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	f.kernel.deps.HostGateway = func(context.Context) (string, error) {
		return "", errors.New("resolve host.k3d.internal: lookup host.k3d.internal on 10.43.0.10:53: no such host")
	}

	result, _ := f.executeDeploymentLocals(t, map[string]deploy.LocalApplication{
		"web": {Ports: map[string]int32{"http": 5173}},
	})
	f.fake.SetFresh()

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue, "the pass re-picks on the health cadence")

	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status, "an unresolved gateway must not fail the run")
	tree, err := f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "waiting", flattenSteps(tree.Steps)["render"])
	require.Empty(t, f.cluster.recorded(), "nothing applies before the desired state renders")

	// The name resolves: the wait closes, the intercepted revision renders
	// and activates on this pass.
	f.kernel.deps.HostGateway = func(context.Context) (string, error) { return "192.0.2.10", nil }
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)
	tree, err = f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", flattenSteps(tree.Steps)["render"])
	require.Contains(t, f.cluster.recorded(), "apply EndpointSlice/"+f.namespace+"/intercept-demo-web-affcdc6d146a6bd037773cb30f69840a")
	target := f.target(t)
	require.NotNil(t, target.ActiveRevisionID)
	require.Equal(t, result.RevisionID, *target.ActiveRevisionID)
}

// Past the rollout deadline an adopted run stops waiting and fails with the
// lookup diagnostic; the target stays so a late resolution still activates.
func TestReconcileHostGatewayUnresolvedDeadline(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Nanosecond})
	ctx := context.Background()
	f.kernel.deps.HostGateway = func(context.Context) (string, error) {
		return "", errors.New("resolve host.k3d.internal: no such host")
	}
	result, _ := f.executeDeploymentLocals(t, map[string]deploy.LocalApplication{
		"web": {Ports: map[string]int32{"http": 5173}},
	})
	f.fake.SetFresh()

	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)
	run, err := f.st.GetRunByID(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	tree, err := f.journal.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", flattenSteps(tree.Steps)["render"])
	target := f.target(t)
	require.Equal(t, result.RevisionID, *target.TargetRevisionID)
	require.Nil(t, target.ActiveRevisionID)
}
