package sqlsession

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	skalikube "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/layout"
)

func target(format Format) *Target {
	return &Target{Namespace: "env-ns", Database: "data", PostgresImage: "postgres:17", Format: format}
}

func TestParseFormat(t *testing.T) {
	for raw, want := range map[string]Format{"": FormatPsql, "psql": FormatPsql, "csv": FormatCSV, "json": FormatJSON} {
		got, err := ParseFormat(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got, raw)
	}
	_, err := ParseFormat("yaml")
	require.ErrorIs(t, err, ErrInvalidOptions)
}

func TestCommand(t *testing.T) {
	wait := []string{"/bin/sh", "-c", readyWait, "sh"}

	interactive := target(FormatPsql)
	interactive.TTY = true
	require.Equal(t, append(wait, "psql", "--no-psqlrc"), Command(interactive))

	require.Equal(t, append(wait, "psql", "--no-psqlrc", "--set", "ON_ERROR_STOP=1", "--pset", "pager=off",
		"--file", "-"), Command(target(FormatPsql)))
	require.Equal(t, append(wait, "psql", "--no-psqlrc", "--set", "ON_ERROR_STOP=1", "--pset", "pager=off",
		"--csv", "--quiet", "--file", "-"), Command(target(FormatCSV)))
	require.Equal(t, []string{"skalid", "sql-runner"}, Command(target(FormatJSON)))
}

func TestRenderJob(t *testing.T) {
	session := target(FormatPsql)
	session.ReadOnly = true
	session.StatementTimeout = 1500 * time.Millisecond
	job := RenderJob("skali-sql-abc", session, "postgres:17", false)

	require.Equal(t, "env-ns", job.Namespace)
	require.Equal(t, map[string]string{LabelSession: "skali-sql-abc"}, job.Labels)
	require.Equal(t, job.Labels, job.Spec.Template.Labels)
	require.NotContains(t, job.Labels, skalikube.LabelManaged, "observation and pruning must never see a client")
	require.EqualValues(t, MaxLifetime/time.Second, *job.Spec.ActiveDeadlineSeconds)
	require.NotNil(t, job.Spec.TTLSecondsAfterFinished)
	require.Zero(t, *job.Spec.BackoffLimit)

	pod := job.Spec.Template.Spec
	require.False(t, *pod.AutomountServiceAccountToken, "the client holds no service account token")
	require.False(t, *pod.EnableServiceLinks)
	require.Equal(t, corev1.RestartPolicyNever, pod.RestartPolicy)
	require.Nil(t, pod.NodeSelector)
	require.True(t, *pod.SecurityContext.RunAsNonRoot)
	require.Len(t, pod.Containers, 1)
	container := pod.Containers[0]
	require.Equal(t, containerName, container.Name)
	require.Equal(t, "postgres:17", container.Image)
	require.Equal(t, []string{"sleep", "28800"}, container.Command)
	require.False(t, *container.SecurityContext.AllowPrivilegeEscalation)

	env := map[string]corev1.EnvVar{}
	for _, variable := range container.Env {
		env[variable.Name] = variable
	}
	secret := skalikube.OutputSecretName("databases", "data")
	for variable, key := range map[string]string{
		"PGHOST": "host", "PGPORT": "port", "PGDATABASE": "name", "PGUSER": "username", "PGPASSWORD": "password",
	} {
		ref := env[variable].ValueFrom.SecretKeyRef
		require.Equal(t, secret, ref.Name, variable)
		require.Equal(t, key, ref.Key, variable)
	}
	require.Equal(t, "-c default_transaction_read_only=on -c statement_timeout=1500", env["PGOPTIONS"].Value)
	require.Empty(t, container.EnvFrom, "only the five connection keys are mounted")

	plain := RenderJob("skali-sql-abc", target(FormatJSON), "skalid:1", true)
	for _, variable := range plain.Spec.Template.Spec.Containers[0].Env {
		require.NotEqual(t, "PGOPTIONS", variable.Name, "no settings, no PGOPTIONS")
	}
	require.Equal(t, map[string]string{
		layout.CapabilityLabel(layout.CapabilityApplication): layout.CapabilityLabelValue,
	}, plain.Spec.Template.Spec.NodeSelector, "managed clusters pin clients to application nodes")
}

func TestPodState(t *testing.T) {
	waiting := func(reason, message string) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
			Name: containerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}},
		}}}}
	}
	for _, tc := range []struct {
		name   string
		pod    *corev1.Pod
		ready  bool
		failed bool
		state  string
	}{
		{"running", &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			Name: containerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}}}, true, false, ""},
		{"pulling", waiting("ContainerCreating", ""), false, false, "ContainerCreating"},
		{"missing secret", waiting("CreateContainerConfigError", `secret "x" not found`), false, true,
			`CreateContainerConfigError: secret "x" not found`},
		{"bad image", waiting("ImagePullBackOff", "back-off"), false, true, "ImagePullBackOff: back-off"},
		{"unschedulable", &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Message: "0/3 nodes are available",
		}}}}, false, false, "not scheduled: 0/3 nodes are available"},
		{"deadline", &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "DeadlineExceeded"}},
			false, true, "the client pod ended before the session started: DeadlineExceeded"},
	} {
		ready, state, failed := podState(tc.pod)
		require.Equal(t, tc.ready, ready, tc.name)
		require.Equal(t, tc.failed, failed, tc.name)
		require.Equal(t, tc.state, state, tc.name)
	}
}

// clientPod is the pod the Job controller would create for the session.
func clientPod(name string, state corev1.ContainerState) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-x1", Namespace: "env-ns", Labels: map[string]string{LabelSession: name}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			Name: containerName, State: state,
		}}},
	}
}

func TestStartWaitsForThePod(t *testing.T) {
	clientset := fake.NewClientset()
	job := RenderJob("skali-sql-abc", target(FormatPsql), "postgres:17", false)

	go func() {
		time.Sleep(3 * pollInterval)
		_, _ = clientset.CoreV1().Pods("env-ns").Create(context.Background(),
			clientPod(job.Name, corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}), metav1.CreateOptions{})
	}()
	pod, err := start(context.Background(), clientset, job)
	require.NoError(t, err)
	require.Equal(t, "skali-sql-abc-x1", pod)
	_, err = clientset.BatchV1().Jobs("env-ns").Get(context.Background(), job.Name, metav1.GetOptions{})
	require.NoError(t, err, "the client job exists")

	deleteJob(context.Background(), clientset, "env-ns", job.Name)
	_, err = clientset.BatchV1().Jobs("env-ns").Get(context.Background(), job.Name, metav1.GetOptions{})
	require.Error(t, err, "teardown deletes the job")
}

func TestStartFailsFast(t *testing.T) {
	clientset := fake.NewClientset()
	job := RenderJob("skali-sql-abc", target(FormatPsql), "postgres:17", false)
	_, err := clientset.CoreV1().Pods("env-ns").Create(context.Background(),
		clientPod(job.Name, corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "ErrImagePull", Message: "manifest unknown",
		}}), metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = start(context.Background(), clientset, job)
	startErr, ok := errors.AsType[*StartError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "start the database client: ErrImagePull: manifest unknown", startErr.Error())
}

func TestStartHonorsCancellation(t *testing.T) {
	clientset := fake.NewClientset()
	job := RenderJob("skali-sql-abc", target(FormatPsql), "postgres:17", false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*pollInterval)
	defer cancel()
	_, err := start(ctx, clientset, job)
	require.ErrorIs(t, err, context.DeadlineExceeded, "a client that went away is not a start failure")
	_, isStart := errors.AsType[*StartError](err)
	require.False(t, isStart)
}

func TestResolveWithoutCluster(t *testing.T) {
	var service *Service
	_, err := service.Resolve(context.Background(), [16]byte{}, Options{Database: "data"})
	require.ErrorIs(t, err, ErrNoCluster)
}
