package observe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"
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
