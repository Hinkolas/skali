package backup

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/store"
)

// shortenWorkerStopWait keeps a refused worker check from waiting out
// the production bound.
func shortenWorkerStopWait(t *testing.T) {
	t.Helper()
	previous := workerStopWait
	workerStopWait = 0
	t.Cleanup(func() { workerStopWait = previous })
}

// workerCluster is the fixture's fake cluster, with Job deletes that can
// be refused the way an unreachable or denying API server refuses them.
type workerCluster struct {
	*fake.Clientset
	namespace  string
	refuseJobs atomic.Bool
}

func (f *holdFixture) workerCluster(t *testing.T) *workerCluster {
	t.Helper()
	clientset, ok := f.controller.deps.Kube.Clientset.(*fake.Clientset)
	require.True(t, ok)
	cluster := &workerCluster{Clientset: clientset, namespace: kubernetes.NamespaceName(f.environmentID.String())}
	clientset.PrependReactor("delete", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		if cluster.refuseJobs.Load() {
			return true, nil, errors.New("the server is currently unable to handle the request")
		}
		return false, nil, nil
	})
	return cluster
}

// leaveWorker puts a running worker of the operation into the
// environment namespace: its Job and the Job's pod.
func (c *workerCluster) leaveWorker(t *testing.T, operationID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	labels := map[string]string{backupJobLabel: operationID.String()}
	_, err := c.BatchV1().Jobs(c.namespace).Create(ctx, &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "restore-" + operationID.String(), Namespace: c.namespace, Labels: labels},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	c.leavePod(t, operationID, nil)
}

// leavePod puts a running pod of the operation into the environment
// namespace, marked for deletion when deleting is set.
func (c *workerCluster) leavePod(t *testing.T, operationID uuid.UUID, deleting *metav1.Time) {
	t.Helper()
	_, err := c.CoreV1().Pods(c.namespace).Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "restore-" + operationID.String() + "-pod", Namespace: c.namespace,
			Labels: map[string]string{backupJobLabel: operationID.String()}, DeletionTimestamp: deleting},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

func (c *workerCluster) workers(t *testing.T) int {
	t.Helper()
	ctx := context.Background()
	jobs, err := c.BatchV1().Jobs(c.namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	pods, err := c.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	return len(jobs.Items) + len(pods.Items)
}

func (f *holdFixture) accept(restoreID uuid.UUID) error {
	_, err := f.controller.AcceptIncompleteRestore(context.Background(), AcceptInput{
		EnvironmentID: f.environmentID, RestoreID: restoreID, Actor: "tester",
	})
	return err
}

// failWithWorkerLeft runs a restore that fails after it stopped the
// environment, while Kubernetes refuses to delete its Job: the row
// concludes failed, its worker keeps running, and the hold stays.
func (f *holdFixture) failWithWorkerLeft(t *testing.T, cluster *workerCluster) store.Backup {
	t.Helper()
	f.bucket = failingStore{f.destination}
	result, err := f.controller.CreateRestore(context.Background(), RestoreInput{
		EnvironmentID: f.environmentID, SnapshotID: f.snapshot(t, "snap", 2), Actor: "tester",
	})
	require.NoError(t, err)
	cluster.leaveWorker(t, result.BackupID)
	cluster.refuseJobs.Store(true)
	require.NoError(t, f.controller.process(context.Background(), result.BackupID))
	row := f.rowStatus(t, result.BackupID)
	require.Equal(t, StatusFailed, row.Status)
	require.Equal(t, row.ID, *f.target(t).IncompleteRestoreID)
	require.Equal(t, 2, cluster.workers(t), "the refused cleanup left the worker running")
	return row
}

// A concluded restore whose Job Kubernetes refused to delete may still be
// writing. Accepting its data is refused and the hold stays; asking again
// retries the cleanup, and once the worker is gone the hold lifts.
func TestAcceptWaitsForTheRestoreWorkers(t *testing.T) {
	shortenWorkerStopWait(t)
	f := newHoldFixture(t)
	cluster := f.workerCluster(t)
	row := f.failWithWorkerLeft(t, cluster)

	err := f.accept(row.ID)
	require.ErrorIs(t, err, ErrRestoreStillRunning)
	var running *WorkersRunningError
	require.ErrorAs(t, err, &running)
	require.ErrorContains(t, err, "delete job")
	require.Equal(t, row.ID, *f.target(t).IncompleteRestoreID, "the hold stays")
	require.True(t, f.slotFree(t), "a refused accept begins no run")

	cluster.refuseJobs.Store(false)
	require.NoError(t, f.accept(row.ID))
	require.Nil(t, f.target(t).IncompleteRestoreID)
	require.Zero(t, cluster.workers(t), "accepting removed the Job and its pod")
}

// A pod still terminating after its Job went (deleted in the background)
// is still a worker: the hold stays until the pod has stopped.
func TestAcceptWaitsForATerminatingWorkerPod(t *testing.T) {
	shortenWorkerStopWait(t)
	f := newHoldFixture(t)
	cluster := f.workerCluster(t)
	f.bucket = failingStore{f.destination}
	_, row := f.restore(t, f.snapshot(t, "snap", 2))
	require.Equal(t, StatusFailed, row.Status)
	cluster.leavePod(t, row.ID, &metav1.Time{Time: time.Now()})

	err := f.accept(row.ID)
	var running *WorkersRunningError
	require.ErrorAs(t, err, &running)
	require.Equal(t, []string{"pod " + cluster.namespace + "/restore-" + row.ID.String() + "-pod"}, running.Left)
	require.Equal(t, row.ID, *f.target(t).IncompleteRestoreID)

	pods := cluster.CoreV1().Pods(cluster.namespace)
	pod, err := pods.Get(context.Background(), "restore-"+row.ID.String()+"-pod", metav1.GetOptions{})
	require.NoError(t, err)
	pod.Status.Phase = corev1.PodFailed
	_, err = pods.UpdateStatus(context.Background(), pod, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.NoError(t, f.accept(row.ID))
	require.Nil(t, f.target(t).IncompleteRestoreID)
}

// A restore over an incomplete one that may still be writing would write
// the same data at once. It is refused before it begins while the held
// restore's worker cannot be removed, and fails before it stops anything
// when the worker shows up after it was accepted. Once the worker is gone,
// a restore completes and lifts the hold.
func TestRestoreWaitsForTheHeldRestoreWorkers(t *testing.T) {
	shortenWorkerStopWait(t)
	f := newHoldFixture(t)
	ctx := context.Background()
	cluster := f.workerCluster(t)
	held := f.failWithWorkerLeft(t, cluster)
	f.bucket = f.destination
	snapshot := f.snapshot(t, "again", 3)

	_, err := f.controller.CreateRestore(ctx, RestoreInput{EnvironmentID: f.environmentID, SnapshotID: snapshot, Actor: "tester"})
	require.ErrorIs(t, err, ErrRestoreStillRunning)
	require.True(t, f.slotFree(t), "a refused restore begins no run")

	cluster.refuseJobs.Store(false)
	result, err := f.controller.CreateRestore(ctx, RestoreInput{EnvironmentID: f.environmentID, SnapshotID: snapshot, Actor: "tester"})
	require.NoError(t, err)
	cluster.leaveWorker(t, held.ID)
	cluster.refuseJobs.Store(true)
	require.NoError(t, f.controller.process(ctx, result.BackupID))
	row := f.rowStatus(t, result.BackupID)
	require.Equal(t, StatusFailed, row.Status)
	require.Contains(t, *row.Error, "the incomplete restore "+held.ID.String()+" may still be writing")
	require.Contains(t, *row.Error, "nothing was changed")
	require.Equal(t, string(journal.RunFailed), f.runStatus(t, result.RunID))
	require.Equal(t, held.ID, *f.target(t).IncompleteRestoreID, "the hold stays with the restore that wrote last")

	cluster.refuseJobs.Store(false)
	_, row = f.restore(t, snapshot)
	require.Equal(t, StatusSucceeded, row.Status)
	require.Nil(t, f.target(t).IncompleteRestoreID)
	require.Zero(t, cluster.workers(t))
}

// A Job's pods carry the operation's label, so a pod that outlives its
// Job is still found and stopped.
func TestJobPodsCarryTheOperationLabel(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	c := New(Deps{Kube: &kube.Client{Clientset: clientset}}, Config{})
	job := renderVolumeJob("restore", "env", "operation", "skalid:1", targetSecretName, "claim", "vol.tar", true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, c.runJob(ctx, nil, job), context.Canceled)

	created, err := clientset.BatchV1().Jobs("env").Get(context.Background(), "restore", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "operation", created.Spec.Template.Labels[backupJobLabel])
}
