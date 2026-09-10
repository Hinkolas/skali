package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
)

// kernelManifestV2 changes the image, so the web application renders a new
// blue-green color beside the one kernelManifest renders.
var kernelManifestV2 = strings.Replace(kernelManifest, "ghcr.io/example/web:1.0.0", "ghcr.io/example/web:1.1.0", 1)

// appliedServiceColor is the color the most recently applied web Service
// selects; "" when the selector is uncolored.
func (f *kernelFixture) appliedServiceColor(t *testing.T) string {
	t.Helper()
	obj := f.cluster.lastApplied("Service/" + f.namespace + "/" + f.webServiceName())
	require.NotNil(t, obj, "the web Service was never applied")
	service, ok := obj.(*corev1.Service)
	require.True(t, ok)
	return service.Spec.Selector[rendering.LabelColor]
}

// observeService mirrors the last applied web Service into the observed
// store, the way the informer would after the apply landed.
func (f *kernelFixture) observeService(t *testing.T) {
	t.Helper()
	obj := f.cluster.lastApplied("Service/" + f.namespace + "/" + f.webServiceName())
	require.NotNil(t, obj)
	service := obj.(*corev1.Service)
	f.fake.SetService(f.environmentID, f.namespace, service.Name, "web", service.Spec.Selector)
}

// deployActiveWithService drives the first deployment to active and mirrors
// its Service into observation, the baseline of every switch scenario.
func (f *kernelFixture) deployActiveWithService(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	f.executeDeployment(t)
	f.fake.SetFresh()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.markHealthy(t)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotNil(t, f.target(t).ActiveRevisionID)
	f.observeService(t)
	return f.webColor(t)
}

func containsOp(ops []string, op string) bool {
	for _, entry := range ops {
		if entry == op {
			return true
		}
	}
	return false
}

// A second revision renders a new color beside the serving one. The
// Service keeps selecting the old color until the new Deployment is fully
// available; then one pass switches it, and the old color outlives the
// switch for the drain window before it is pruned.
func TestBlueGreenSwitchWaitsForAvailability(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour, RetireDrain: time.Hour})
	ctx := context.Background()
	oldColor := f.deployActiveWithService(t)
	oldName := f.webDeploymentName(t)
	require.Equal(t, oldColor, f.appliedServiceColor(t), "a first deploy selects its own color")

	second := f.executeDeploymentManifest(t, kernelManifestV2)
	newColor := f.webColor(t)
	newName := f.webDeploymentName(t)
	require.NotEqual(t, oldColor, newColor)
	require.NotEqual(t, oldName, newName)

	f.cluster.ops = nil
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)
	ops := f.cluster.recorded()
	require.True(t, containsOp(ops, "apply Deployment/"+f.namespace+"/"+newName), "the new color is applied beside the old one")
	require.Equal(t, oldColor, f.appliedServiceColor(t), "traffic stays on the serving color while the new one starts")
	require.False(t, containsOp(ops, "delete Deployment/"+f.namespace+"/"+oldName), "the serving color is never pruned")
	active := f.target(t).ActiveRevisionID
	require.True(t, active == nil || *active != second.RevisionID, "not active yet")

	// The new color becomes fully available: this pass switches the Service.
	f.setWebWorkload(t, "", module.WorkloadStatus{Desired: 1, Ready: 1, Updated: 1, Available: 1})
	f.cluster.ops = nil
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, newColor, f.appliedServiceColor(t), "a fully available color takes traffic in one step")
	require.False(t, containsOp(f.cluster.recorded(), "delete Deployment/"+f.namespace+"/"+oldName),
		"the previous color drains before it is pruned")
	require.NotEqual(t, second.RevisionID, *f.target(t).ActiveRevisionID,
		"activation waits until the switch is observed")

	// Observation catches up with the switch: the revision activates and the
	// pass requeues for the pending retirement instead of going idle.
	f.observeService(t)
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, second.RevisionID, *f.target(t).ActiveRevisionID)
	require.Greater(t, requeue, time.Duration(0), "a retiring color keeps the environment scheduled")
	require.LessOrEqual(t, requeue, time.Hour)

	// The drain window passes: the retired color is pruned and the
	// environment goes idle.
	f.kernel.cfg.RetireDrain = time.Nanosecond
	f.cluster.ops = nil
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.True(t, containsOp(f.cluster.recorded(), "delete Deployment/"+f.namespace+"/"+oldName))
	require.Zero(t, requeue)
}

// A pending color superseded by yet another revision never carried traffic;
// it retires like any other non-serving Deployment while the serving color
// stays protected until its replacement is available.
func TestBlueGreenSupersededColorRetires(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour, RetireDrain: time.Hour})
	ctx := context.Background()
	oldColor := f.deployActiveWithService(t)
	oldName := f.webDeploymentName(t)

	second := f.executeDeploymentManifest(t, kernelManifestV2)
	pendingName := f.webDeploymentName(t)
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	// The pending color exists on the cluster but is not available; the
	// operator gives up on it (one running run per environment).
	f.setWebWorkload(t, "", module.WorkloadStatus{Desired: 1, Ready: 0, Updated: 1})
	require.NoError(t, f.journal.FinishRun(ctx, second.RunID, journal.RunCancelled))

	third := strings.Replace(kernelManifest, "ghcr.io/example/web:1.0.0", "ghcr.io/example/web:1.2.0", 1)
	f.executeDeploymentManifest(t, third)
	thirdName := f.webDeploymentName(t)
	require.NotEqual(t, pendingName, thirdName)

	// First sight arms the retirement timer; the next pass past the drain
	// window prunes.
	f.kernel.cfg.RetireDrain = time.Nanosecond
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	f.cluster.ops = nil
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	ops := f.cluster.recorded()
	require.True(t, containsOp(ops, "apply Deployment/"+f.namespace+"/"+thirdName))
	require.True(t, containsOp(ops, "delete Deployment/"+f.namespace+"/"+pendingName), "the superseded pending color retires")
	require.False(t, containsOp(ops, "delete Deployment/"+f.namespace+"/"+oldName), "the serving color is protected")
	require.Equal(t, oldColor, f.appliedServiceColor(t))
}

// An environment deployed before blue-green runs an uncolored Deployment
// behind an uncolored Service. The first blue-green deploy keeps the
// selector uncolored while the new color starts, colors it once the new
// Deployment is available, and retires the legacy workload after the drain.
func TestBlueGreenLegacyMigration(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour, RetireDrain: time.Hour})
	ctx := context.Background()
	legacyName := rendering.ApplicationName("demo", "web")
	f.fake.SetWorkload(f.environmentID, f.namespace, legacyName, "web", "",
		module.WorkloadStatus{Desired: 1, Ready: 1, Updated: 1, Available: 1})
	f.fake.SetService(f.environmentID, f.namespace, f.webServiceName(), "web", map[string]string{
		"app.kubernetes.io/name": legacyName, rendering.LabelManaged: "true",
		rendering.LabelProject: "demo", rendering.LabelApplication: "web",
	})

	f.executeDeployment(t)
	f.fake.SetFresh()
	newName := f.webDeploymentName(t)
	newColor := f.webColor(t)
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	ops := f.cluster.recorded()
	require.True(t, containsOp(ops, "apply Deployment/"+f.namespace+"/"+newName))
	require.Empty(t, f.appliedServiceColor(t), "the legacy Deployment keeps serving through the uncolored selector")
	require.False(t, containsOp(ops, "delete Deployment/"+f.namespace+"/"+legacyName))

	f.setWebWorkload(t, "", module.WorkloadStatus{Desired: 1, Ready: 1, Updated: 1, Available: 1})
	f.cluster.ops = nil
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, newColor, f.appliedServiceColor(t), "the selector gains the color once the new Deployment is available")
	require.False(t, containsOp(f.cluster.recorded(), "delete Deployment/"+f.namespace+"/"+legacyName), "drain first")

	f.observeService(t)
	f.kernel.cfg.RetireDrain = time.Nanosecond
	f.cluster.ops = nil
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.True(t, containsOp(f.cluster.recorded(), "delete Deployment/"+f.namespace+"/"+legacyName))
	require.NotNil(t, f.target(t).ActiveRevisionID)
}

// A new color that never becomes available never receives traffic: the
// deadline falls the target back to the active revision, whose color is the
// serving one, so the Service is untouched and the failed color retires.
func TestBlueGreenFailedColorNeverServes(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Nanosecond, RetireDrain: time.Hour})
	ctx := context.Background()
	oldColor := f.deployActiveWithService(t)
	first := *f.target(t).ActiveRevisionID

	second := f.executeDeploymentManifest(t, kernelManifestV2)
	failedName := f.webDeploymentName(t)
	f.cluster.ops = nil
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue, "the fallback re-enqueues directly")
	require.True(t, containsOp(f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+failedName))
	require.Equal(t, oldColor, f.appliedServiceColor(t))

	target := f.target(t)
	require.Equal(t, first, *target.TargetRevisionID, "the target fell back to the active revision")
	run, err := f.st.GetRunByID(ctx, second.RunID)
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)

	// The re-rendered active revision selects the serving color: a no-op for
	// the Service, and the failed color retires after the drain.
	f.setWebWorkload(t, "", module.WorkloadStatus{Desired: 1, Ready: 1, Updated: 1, Available: 1})
	f.fake.SetColoredWorkload(f.environmentID, f.namespace, failedName, "web", "", "deadbeef00",
		module.WorkloadStatus{Desired: 1, Ready: 0, Updated: 1})
	f.kernel.cfg.RetireDrain = time.Nanosecond
	f.cluster.ops = nil
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, oldColor, f.appliedServiceColor(t), "traffic never moved")
	require.False(t, containsOp(f.cluster.recorded(), "delete Deployment/"+f.namespace+"/"+failedName),
		"first sight arms the drain window")
	f.cluster.ops = nil
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.True(t, containsOp(f.cluster.recorded(), "delete Deployment/"+f.namespace+"/"+failedName))
}

// A restart stamps the pod template, which is a new color: the restart
// becomes an atomic switch too, on an environment whose target is already
// active, and the pass still schedules the retirement.
func TestBlueGreenRestartSwapsColor(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour, RetireDrain: time.Hour})
	ctx := context.Background()
	oldColor := f.deployActiveWithService(t)
	oldName := f.webDeploymentName(t)

	_, err := f.deploy.Restart(ctx, deploy.RestartInput{
		EnvironmentID: f.environmentID, ApplicationKey: "web", Actor: "tester", Journal: f.journal,
	})
	require.NoError(t, err)
	newName := f.webDeploymentName(t)
	require.NotEqual(t, oldName, newName, "a restart stamp renders a new color")

	f.cluster.ops = nil
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.True(t, containsOp(f.cluster.recorded(), "apply Deployment/"+f.namespace+"/"+newName))
	require.Equal(t, oldColor, f.appliedServiceColor(t))

	f.setWebWorkload(t, "", module.WorkloadStatus{Desired: 1, Ready: 1, Updated: 1, Available: 1})
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, f.webColor(t), f.appliedServiceColor(t))
	f.observeService(t)
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Greater(t, requeue, time.Duration(0), "the previous color's retirement is scheduled")
}
