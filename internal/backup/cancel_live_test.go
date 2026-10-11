package backup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
)

// TestLiveCancelInterruptsJobWait cancels a volume backup while it waits on
// a real Job: the claim it archives does not exist, so the Job's pod stays
// pending and only the cancel ends the wait. The run and the row conclude
// cancelled, the Job and the operation's target Secret leave the cluster,
// and the environment's run slot frees. Requires TEST_KUBECONFIG and
// TEST_DATABASE_URL.
func TestLiveCancelInterruptsJobWait(t *testing.T) {
	config := kubetest.Config(t)
	f := newServiceFixture(t)
	ctx := context.Background()
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	f.controller.deps.Kube = client
	// Never pulled: the pod cannot schedule without its claim.
	f.controller.cfg.WorkerImage = "registry.invalid/skali-backup-worker:never"
	f.controller.openStore = func(s3Location) (objectStore, error) { return newMemoryStore(), nil }
	f.configureTarget(t)
	f.activate(t, compiler.ProjectDefinition{
		Schema: compiler.DefinitionSchema, Name: "demo",
		Applications: map[string]compiler.Application{"web": {
			Volumes: map[string]compiler.Volume{"data": {MountPath: "/data", SizeBytes: 1 << 30}},
		}},
	})

	namespace := kubernetes.RenderNamespace("demo", "production", f.environmentID.String())
	_, err = client.Apply(ctx, namespace, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.Clientset.CoreV1().Namespaces().Delete(context.Background(), namespace.Name, metav1.DeleteOptions{})
	})
	jobs := client.Clientset.BatchV1().Jobs(namespace.Name)

	result, err := f.controller.CreateBackup(ctx, BackupInput{EnvironmentID: f.environmentID, Actor: "tester"})
	require.NoError(t, err)
	selector := metav1.ListOptions{LabelSelector: backupJobLabel + "=" + result.BackupID.String()}
	done := make(chan error, 1)
	go func() { done <- f.controller.process(ctx, result.BackupID) }()

	require.Eventually(t, func() bool {
		list, err := jobs.List(ctx, selector)
		return err == nil && len(list.Items) == 1
	}, time.Minute, 500*time.Millisecond, "the backup must create its Job")
	_, err = client.Clientset.CoreV1().Secrets(namespace.Name).Get(ctx, targetSecretName, metav1.GetOptions{})
	require.NoError(t, err, "the Job's target Secret exists while it runs")
	// Several polls of the pending Job: the wait is real.
	time.Sleep(3 * jobPollInterval)
	require.False(t, f.slotFree(t))

	require.NoError(t, f.controller.Cancel(ctx, result.RunID))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Minute):
		t.Fatal("the cancel did not interrupt the Job wait")
	}
	require.Equal(t, string(journal.RunCancelled), f.runStatus(t, result.RunID))
	require.Equal(t, StatusCancelled, f.rowStatus(t, result.BackupID).Status)
	require.True(t, f.slotFree(t))

	require.Eventually(t, func() bool {
		list, err := jobs.List(ctx, selector)
		return err == nil && len(list.Items) == 0
	}, time.Minute, time.Second, "the cancelled backup's Job must leave the cluster")
	_, err = client.Clientset.CoreV1().Secrets(namespace.Name).Get(ctx, targetSecretName, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "the target Secret must leave the cluster")
}
