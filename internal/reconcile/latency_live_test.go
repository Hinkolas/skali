package reconcile

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/Hinkolas/skali/internal/journal"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/project"
)

// promptBound is how long a rollout may wait at either handoff while other
// environments keep the workers busy: the plan's p95 target. Unloaded, both
// handoffs take a fraction of a second.
const promptBound = 5 * time.Second

// switchWatch records when the first pod outside a known set turned Ready
// and when the Service's selector first moved off a color, as the watches
// deliver them.
type switchWatch struct {
	mu       sync.Mutex
	ready    time.Time
	switched time.Time
}

func (w *switchWatch) times() (ready, switched time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ready, w.switched
}

// watchSwitch starts watching the fixture's web pods and Service from now:
// pods in old and the Service selecting fromColor are the serving state.
func (f *liveFixture) watchSwitch(t *testing.T, ctx context.Context, old map[types.UID]bool, fromColor string) *switchWatch {
	t.Helper()
	recorded := &switchWatch{}
	pods, err := f.clientset.CoreV1().Pods(f.namespace).Watch(ctx, metav1.ListOptions{
		LabelSelector: rendering.LabelApplication + "=web",
	})
	require.NoError(t, err)
	services, err := f.clientset.CoreV1().Services(f.namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + rendering.ApplicationName(f.projectName, "web"),
	})
	require.NoError(t, err)
	t.Cleanup(pods.Stop)
	t.Cleanup(services.Stop)
	go func() {
		for {
			var event watch.Event
			var open bool
			select {
			case event, open = <-pods.ResultChan():
			case event, open = <-services.ResultChan():
			}
			if !open {
				return
			}
			at := time.Now()
			recorded.mu.Lock()
			switch object := event.Object.(type) {
			case *corev1.Pod:
				if recorded.ready.IsZero() && !old[object.UID] && podReady(object) {
					recorded.ready = at
				}
			case *corev1.Service:
				if recorded.switched.IsZero() && object.Spec.Selector[rendering.LabelColor] != fromColor {
					recorded.switched = at
				}
			}
			recorded.mu.Unlock()
		}
	}()
	return recorded
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// Live: a deploy amid background work is still prompt. While eight other
// environments keep both workers busy, each pass reading live as after an
// audit (the heaviest pass there is), one environment promotes a new
// revision: its first pass follows the promotion within the bound, and its
// traffic switches within the bound of its new pod turning Ready. Missing
// wakeups would show as the 15-second health requeue or the 5-minute
// resync.
func TestLiveDeployAmidBackgroundWorkIsPrompt(t *testing.T) {
	t.Parallel()
	// The daemon's default request budget, not client-go's 5 per second:
	// the bound is the installation's, not a throttled test client's.
	config := kubetest.Config(t)
	config.QPS, config.Burst = 50, 100
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute}, config)
	f.start(t)
	ctx := context.Background()

	projects := project.New(f.st)
	environments := []uuid.UUID{f.environmentID}
	for i := range 8 {
		env, err := projects.CreateEnvironment(ctx, f.projectID, fmt.Sprintf("background-%d", i), project.EnvironmentOptions{})
		require.NoError(t, err)
		namespace := "skali-" + env.ID.String()
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = f.clientset.CoreV1().Namespaces().Delete(cleanupCtx, namespace, metav1.DeleteOptions{})
		})
		environments = append(environments, env.ID)
	}
	manifest := liveManifest(f.projectName, 1, false)
	for _, environmentID := range environments {
		result := f.deployTo(t, environmentID, manifest)
		f.waitActiveIn(t, environmentID, result.RevisionID, 3*time.Minute)
	}
	serving := f.webService(t).Spec.Selector[rendering.LabelColor]
	require.NotEmpty(t, serving)
	old := map[types.UID]bool{}
	pods, err := f.clientset.CoreV1().Pods(f.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: rendering.LabelApplication + "=web",
	})
	require.NoError(t, err)
	for _, pod := range pods.Items {
		old[pod.UID] = true
	}
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	recorded := f.watchSwitch(t, watchCtx, old, serving)

	// The background load: every environment owes a live read, and every
	// other one is queued again every 50 ms, which keeps both workers busy,
	// until the rollout concluded. The rolling-out environment is left to
	// its own wakeups, the promotion's and the watches'.
	loadCtx, stopLoad := context.WithCancel(ctx)
	loaded := make(chan struct{})
	go func() {
		defer close(loaded)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			f.kernel.liveMu.Lock()
			clear(f.kernel.liveRead)
			f.kernel.liveMu.Unlock()
			for _, environmentID := range environments[1:] {
				f.kernel.EnqueueFor(environmentID, ReasonAudit)
			}
			select {
			case <-loadCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	time.Sleep(time.Second) // the load is underway before the promotion

	changed := "name: " + f.projectName + "\napplications:\n  web:\n" +
		"    image: traefik/whoami:v1.10.2\n" +
		"    ports:\n      http:\n        port: 80\n        protocol: http\n" +
		"    environment:\n      ROLLOUT: second\n"
	second := f.deployTo(t, f.environmentID, changed)
	f.waitActive(t, second.RevisionID, 3*time.Minute)
	stopLoad()
	<-loaded

	tree, err := f.journal.RunTree(ctx, second.RunID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", tree.Run.Status)
	stepTimes := map[string]*time.Time{}
	for _, node := range tree.Steps {
		switch node.Step.Key {
		case "promote":
			stepTimes["promote"] = node.Step.FinishedAt
		case "rollout":
			stepTimes["rollout"] = node.Step.StartedAt
		}
	}
	require.NotNil(t, stepTimes["promote"])
	require.NotNil(t, stepTimes["rollout"])
	toFirstPass := stepTimes["rollout"].Sub(*stepTimes["promote"])

	require.Eventually(t, func() bool {
		ready, switched := recorded.times()
		return !ready.IsZero() && !switched.IsZero()
	}, 10*time.Second, 50*time.Millisecond, "the watches saw the new pod Ready and the Service switch")
	ready, switched := recorded.times()
	readyToSwitch := switched.Sub(ready)
	var walk func(nodes []*journal.TreeStep)
	walk = func(nodes []*journal.TreeStep) {
		for _, node := range nodes {
			if node.Step.StartedAt != nil {
				t.Logf("  %-40s started %+.2fs", node.Step.Key, node.Step.StartedAt.Sub(*stepTimes["promote"]).Seconds())
			}
			if node.Step.FinishedAt != nil {
				t.Logf("  %-40s ended   %+.2fs", node.Step.Key, node.Step.FinishedAt.Sub(*stepTimes["promote"]).Seconds())
			}
			walk(node.Children)
		}
	}
	walk(tree.Steps)
	t.Logf("  ready seen %+.2fs, switch seen %+.2fs, run finished %+.2fs",
		ready.Sub(*stepTimes["promote"]).Seconds(), switched.Sub(*stepTimes["promote"]).Seconds(),
		tree.Run.FinishedAt.Sub(*stepTimes["promote"]).Seconds())
	t.Logf("amid %d environments passed every 50 ms: promotion to first pass %s, Ready to switch %s",
		len(environments)-1, toFirstPass, readyToSwitch)
	require.Less(t, toFirstPass, promptBound, "promotion to the rollout's first pass")
	require.Less(t, readyToSwitch, promptBound, "the new pod turning Ready to the traffic switch")
}
