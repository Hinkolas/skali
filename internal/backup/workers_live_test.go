package backup

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
)

// TestLiveStopWorkersWaitsForTheirPods interrupts a real Job whose worker
// ignores SIGTERM, the way a cancel interrupts a restore's Job wait. Right
// after the delete, the Job (deleted in the foreground) and its
// terminating pod are still there and the workers count as running; once
// the pod's grace period ends both are gone and stopWorkers reports the
// workers stopped. Requires TEST_KUBECONFIG.
func TestLiveStopWorkersWaitsForTheirPods(t *testing.T) {
	config := kubetest.Config(t)
	ctx := context.Background()
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	c := New(Deps{Kube: client}, Config{})

	environmentID := uuid.New()
	namespace := kubernetes.RenderNamespace("demo", "production", environmentID.String())
	_, err = client.Apply(ctx, namespace, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.Clientset.CoreV1().Namespaces().Delete(context.Background(), namespace.Name, metav1.DeleteOptions{})
	})

	operationID := uuid.New()
	grace := int64(5)
	job := &batchv1.Job{
		ObjectMeta: jobMeta("worker", namespace.Name, operationID.String()),
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: &grace,
			Containers: []corev1.Container{{
				Name:    "worker",
				Image:   "ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie",
				Command: []string{"/bin/sh", "-c", "trap '' TERM; while true; do sleep 1; done"},
			}},
		}}},
	}
	waiting, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- c.runJob(waiting, nil, job) }()
	pods := client.Clientset.CoreV1().Pods(namespace.Name)
	selector := metav1.ListOptions{LabelSelector: backupJobLabel + "=" + operationID.String()}
	require.Eventually(t, func() bool {
		list, err := pods.List(ctx, selector)
		return err == nil && len(list.Items) == 1 && list.Items[0].Status.Phase == corev1.PodRunning
	}, 2*time.Minute, time.Second, "the worker pod must run, found by the operation's label")
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	previous := workerStopWait
	t.Cleanup(func() { workerStopWait = previous })
	workerStopWait = 0
	err = c.stopWorkers(ctx, operationID, environmentID)
	var running *WorkersRunningError
	require.ErrorAs(t, err, &running)
	require.NoError(t, running.Err)
	require.Contains(t, running.Left, "job "+namespace.Name+"/worker", "a Job deleted in the foreground waits for its pod")

	workerStopWait = 30 * time.Second
	require.NoError(t, c.stopWorkers(ctx, operationID, environmentID))
	list, err := pods.List(ctx, selector)
	require.NoError(t, err)
	require.Empty(t, list.Items)
}
