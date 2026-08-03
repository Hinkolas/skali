package observe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/Hinkolas/skali/internal/layout"
)

func testKubeSource() *KubeSource {
	store := NewStore(nil)
	store.RegisterSource(SourceKubernetes)
	return &KubeSource{store: store}
}

func TestTrackContactForwardsEvents(t *testing.T) {
	k := testKubeSource()
	inner := watch.NewFake()
	tracked := k.trackContact("Test", inner)
	go inner.Add(&corev1.Pod{})
	event := <-tracked.ResultChan()
	require.Equal(t, watch.Added, event.Type)
	inner.Stop()
	_, open := <-tracked.ResultChan()
	require.False(t, open, "the forwarder must close with the inner watcher")
}

func TestRefreshStopsCurrentWatchers(t *testing.T) {
	k := testKubeSource()
	first := watch.NewFake()
	second := watch.NewFake()
	trackedFirst := k.trackContact("KindA", first)
	trackedSecond := k.trackContact("KindB", second)

	k.Refresh()
	require.Eventually(t, first.IsStopped, time.Second, 5*time.Millisecond)
	require.Eventually(t, second.IsStopped, time.Second, 5*time.Millisecond)
	_, open := <-trackedFirst.ResultChan()
	require.False(t, open)
	_, open = <-trackedSecond.ResultChan()
	require.False(t, open)

	// The reflector reconnects; the replacement connection must not be
	// touched by a rate-limited repeat call.
	replacement := watch.NewFake()
	_ = k.trackContact("KindA", replacement)
	k.Refresh()
	time.Sleep(50 * time.Millisecond)
	require.False(t, replacement.IsStopped(), "refresh must be rate-limited")
	replacement.Stop()
}

func TestNodeRecordProjection(t *testing.T) {
	heartbeat := metav1.NewTime(time.Unix(1700000000, 0))
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cp-1",
			Labels: map[string]string{
				layout.ControlPlaneLabel:                 "true",
				layout.CapabilityLabelPrefix + "edge":    layout.CapabilityLabelValue,
				layout.CapabilityLabelPrefix + "builder": layout.CapabilityLabelValue,
			},
		},
		Spec: corev1.NodeSpec{Unschedulable: false},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				Architecture:   "arm64",
				OSImage:        "Ubuntu 24.04 LTS",
				KubeletVersion: "v1.31.4+k3s1",
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: heartbeat},
			},
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
				{Type: corev1.NodeExternalIP, Address: "203.0.113.1"},
			},
		},
	}

	record := nodeRecord(node)
	require.Equal(t, "cp-1", record.Name)
	require.Equal(t, layout.RoleServer, record.Role)
	require.Equal(t, []string{"builder", "edge"}, record.Capabilities, "capabilities sort")
	require.Equal(t, "arm64", record.Arch)
	require.Equal(t, "Ubuntu 24.04 LTS", record.OS)
	require.Equal(t, "v1.31.4+k3s1", record.KubeletVersion)
	require.True(t, record.Ready)
	require.True(t, record.Schedulable)
	require.Equal(t, "10.0.0.1", record.InternalIP)
	require.Equal(t, "203.0.113.1", record.ExternalIP)
	require.Equal(t, heartbeat.Time, record.LastHeartbeat)

	// A bare agent node: not ready, cordoned, nothing reported yet.
	record = nodeRecord(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "db-1"},
		Spec:       corev1.NodeSpec{Unschedulable: true},
	})
	require.Equal(t, layout.RoleAgent, record.Role)
	require.False(t, record.Ready)
	require.False(t, record.Schedulable)
	require.True(t, record.LastHeartbeat.IsZero())
	require.Empty(t, record.Capabilities)
}

func TestRefreshSkipsEndedConnections(t *testing.T) {
	// A connection that ended on its own must leave the registry: refresh
	// must only stop the kind's CURRENT connection, not a stale handle.
	k := testKubeSource()
	ended := watch.NewFake()
	tracked := k.trackContact("KindA", ended)
	ended.Stop()
	_, open := <-tracked.ResultChan()
	require.False(t, open)

	k.mu.Lock()
	_, registered := k.current["KindA"]
	k.mu.Unlock()
	require.False(t, registered, "an ended connection must deregister itself")
}
