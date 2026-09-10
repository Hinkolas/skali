package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
)

// seedObject records an arbitrary observed object; the returned ref removes
// it again when the test simulates the watch delete event.
func (f *kernelFixture) seedObject(gvk schema.GroupVersionKind, kind, namespace, name, service string) kube.ObjectRef {
	ref := kube.ObjectRef{GVK: gvk, Namespace: namespace, Name: name, UID: types.UID("uid-" + name)}
	f.fake.Upsert(observe.Object{
		Ref: ref, Kind: kind, Name: name,
		Environment: f.environmentID, Service: service,
	})
	return ref
}

// deployedAndActive drives the fixture to a healthy, activated deployment
// and seeds the surrounding observed objects a live environment would have.
func (f *kernelFixture) deployedAndActive(t *testing.T) (serviceRef, volumeRef, namespaceRef kube.ObjectRef) {
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

	serviceRef = f.seedObject(schema.GroupVersionKind{Version: "v1", Kind: "Service"},
		module.KindService, f.namespace, f.webServiceName(), "web")
	volumeRef = f.seedObject(schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaim"},
		module.KindVolume, f.namespace, "demo-web-data", "web")
	namespaceRef = f.seedObject(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"},
		"namespace", "", f.namespace, "")
	return serviceRef, volumeRef, namespaceRef
}

// workloadRef names the web Deployment by the name captured while the
// environment still had a target: teardown clears the pointers the fixture
// would otherwise derive the name from.
func (f *kernelFixture) workloadRef(name string) kube.ObjectRef {
	return kube.ObjectRef{
		GVK:       schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Namespace: f.namespace, Name: name,
	}
}

func (f *kernelFixture) podRef() kube.ObjectRef {
	return kube.ObjectRef{
		GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
		Namespace: f.namespace, Name: "web-1",
	}
}

func TestTeardownDownRemovesWorkloadsAndKeepsData(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	serviceRef, _, _ := f.deployedAndActive(t)
	deploymentName := f.webDeploymentName(t)

	run, err := f.deploy.Teardown(ctx, f.environmentID, false, f.journal, "tester")
	require.NoError(t, err)

	target := f.target(t)
	require.Equal(t, deploy.EnvironmentStateDown, target.State)
	require.Nil(t, target.TargetRevisionID, "down clears the target pointer")
	require.Nil(t, target.ActiveRevisionID, "down clears the active pointer")

	f.cluster.ops = nil
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue, "deletion is asynchronous")

	ops := f.cluster.recorded()
	require.Contains(t, ops, "delete Deployment/"+f.namespace+"/"+deploymentName)
	require.Contains(t, ops, "delete Service/"+f.namespace+"/"+f.webServiceName())
	require.Contains(t, ops, "delete Secret/"+f.namespace+"/skali-environment")
	for _, op := range ops {
		require.NotContains(t, op, "PersistentVolumeClaim", "down never touches volumes")
		require.NotContains(t, op, "Namespace/", "down never touches the namespace")
	}

	// The watch delete events arrive; the next pass settles and concludes
	// the teardown run. Volumes and the namespace remain observed.
	f.fake.Remove(f.workloadRef(deploymentName))
	f.fake.Remove(f.podRef())
	f.fake.Remove(serviceRef)
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)
	row, err := f.st.GetRunByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", row.Status)

	// A repeat pass is silent: nothing to delete, nothing to journal.
	f.cluster.ops = nil
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Empty(t, f.cluster.recorded())

	// The environment row and its data survived; the next promotion (the
	// guarded target write every deployment ends in) resurrects it.
	_, err = f.st.GetEnvironmentByID(ctx, f.environmentID)
	require.NoError(t, err)
	revisionID := f.revisionID
	rows, err := f.st.SetEnvironmentTarget(ctx, store.SetEnvironmentTargetParams{
		EnvironmentID: f.environmentID, TargetRevisionID: &revisionID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows, "a promote resurrects a down environment")
	require.Equal(t, deploy.EnvironmentStateActive, f.target(t).State)
}

// A completed release Job outlives the workloads on purpose (it is the
// per-revision already-ran marker), so its terminal pod must not hold a
// plain down open; a release pod still running must.
func TestTeardownDownIgnoresTerminalReleasePod(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	serviceRef, _, _ := f.deployedAndActive(t)
	deploymentName := f.webDeploymentName(t)

	f.fake.SetReleaseJob(f.environmentID, f.namespace, "demo-web-release-abc123", "web",
		observe.JobStatus{Succeeded: true, Created: time.Now()})
	f.fake.SetPod(f.environmentID, f.namespace, rendering.ReleaseServiceIdentity("web"),
		"demo-web-release-abc123-x9", "node-a", module.PodStatus{Phase: "Running"})

	run, err := f.deploy.Teardown(ctx, f.environmentID, false, f.journal, "tester")
	require.NoError(t, err)

	f.cluster.ops = nil
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)
	for _, op := range f.cluster.recorded() {
		require.NotContains(t, op, "release", "down never touches the release plane")
	}

	// The application objects disappear, but the release command is still
	// running: the down must keep waiting for it.
	f.fake.Remove(f.workloadRef(deploymentName))
	f.fake.Remove(f.podRef())
	f.fake.Remove(serviceRef)
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue, "a running release pod holds the down open")
	row, err := f.st.GetRunByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "running", row.Status)

	// The release pod reaching a terminal phase settles the down; the pod
	// itself stays observed as part of the kept release plane.
	f.fake.SetPod(f.environmentID, f.namespace, rendering.ReleaseServiceIdentity("web"),
		"demo-web-release-abc123-x9", "node-a", module.PodStatus{Phase: "Succeeded"})
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)
	row, err = f.st.GetRunByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", row.Status)
}

func TestTeardownPurgeRemovesEverything(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{RolloutDeadline: time.Hour})
	ctx := context.Background()
	serviceRef, volumeRef, namespaceRef := f.deployedAndActive(t)
	deploymentName := f.webDeploymentName(t)

	run, err := f.deploy.Teardown(ctx, f.environmentID, true, f.journal, "tester")
	require.NoError(t, err)
	require.Equal(t, deploy.EnvironmentStateReleasing, f.target(t).State)

	// Releasing is one-way: no down, no promote.
	_, err = f.deploy.Teardown(ctx, f.environmentID, false, f.journal, "tester")
	require.ErrorIs(t, err, deploy.ErrEnvironmentReleasing)
	revisionID := f.revisionID
	rows, err := f.st.SetEnvironmentTarget(ctx, store.SetEnvironmentTargetParams{
		EnvironmentID: f.environmentID, TargetRevisionID: &revisionID,
	})
	require.NoError(t, err)
	require.Zero(t, rows, "promoting into a releasing environment must miss")

	f.cluster.ops = nil
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, requeueHealthCheck, requeue)
	ops := f.cluster.recorded()
	require.Contains(t, ops, "delete Deployment/"+f.namespace+"/"+deploymentName)
	require.Contains(t, ops, "delete PersistentVolumeClaim/"+f.namespace+"/demo-web-data")
	require.Contains(t, ops, "delete Namespace/"+f.namespace)

	// Everything disappears from observation; the final pass concludes the
	// run, then deletes the environment row, cascading all its data.
	f.fake.Remove(f.workloadRef(deploymentName))
	f.fake.Remove(f.podRef())
	f.fake.Remove(serviceRef)
	f.fake.Remove(volumeRef)
	f.fake.Remove(namespaceRef)
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)

	_, err = f.st.GetEnvironmentByID(ctx, f.environmentID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "the purge deletes the environment row")
	_, err = f.st.GetEnvironmentTarget(ctx, f.environmentID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "the target row cascades")
	_, err = f.st.GetRunByID(ctx, run.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "the journal cascades; it is explanatory only")

	// A stray enqueue after the row is gone stays a no-op.
	requeue, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)
}

func TestTeardownNeverDeployedSettlesImmediately(t *testing.T) {
	t.Parallel()
	f := newKernelFixture(t, Config{})
	ctx := context.Background()
	f.fake.SetFresh()

	run, err := f.deploy.Teardown(ctx, f.environmentID, false, f.journal, "tester")
	require.NoError(t, err)
	requeue, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	require.Zero(t, requeue)
	require.Empty(t, f.cluster.recorded(), "nothing observed, nothing deleted")
	row, err := f.st.GetRunByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", row.Status)

	// Purging the empty environment removes the row on the first pass.
	_, err = f.deploy.Teardown(ctx, f.environmentID, true, f.journal, "tester")
	require.NoError(t, err)
	_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	_, err = f.st.GetEnvironmentByID(ctx, f.environmentID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
}
