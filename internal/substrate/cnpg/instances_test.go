package cnpg

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInstancesFromPods(t *testing.T) {
	started := metav1.NewTime(time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC))
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pg17-shared-2", Labels: map[string]string{LabelRoleLegacy: RoleReplica}},
			Spec:       corev1.PodSpec{NodeName: "db-2"},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
				ContainerStatuses: []corev1.ContainerStatus{
					{RestartCount: 2}, {RestartCount: 1},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pg17-shared-1", Labels: map[string]string{LabelInstanceRole: RolePrimary}},
			Spec:       corev1.PodSpec{NodeName: "db-1"},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				StartTime:  &started,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pg17-shared-3"},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		},
	}
	got := InstancesFromPods(pods)
	require.Len(t, got, 3)

	require.Equal(t, "pg17-shared-1", got[0].Name)
	require.Equal(t, RolePrimary, got[0].Role)
	require.Equal(t, "db-1", got[0].Node)
	require.True(t, got[0].Ready)
	require.Equal(t, started.Time, *got[0].StartedAt)

	require.Equal(t, "pg17-shared-2", got[1].Name)
	require.Equal(t, RoleReplica, got[1].Role, "legacy role label is the fallback")
	require.False(t, got[1].Ready)
	require.Equal(t, int32(3), got[1].Restarts)
	require.Nil(t, got[1].StartedAt)

	require.Equal(t, "pg17-shared-3", got[2].Name)
	require.Equal(t, "", got[2].Role)
	require.Equal(t, "Pending", got[2].Phase)

	require.Equal(t, "cnpg.io/cluster=pg17-shared,cnpg.io/podRole=instance", InstanceSelector("pg17-shared"))
	require.Empty(t, InstancesFromPods(nil))
}
