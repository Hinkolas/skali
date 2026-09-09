package hostdaemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/version"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReleaseVerificationRequiresFreshReportsFromBothHostProcesses(t *testing.T) {
	now := time.Now()
	release := "v0.1.0-alpha.5"
	k3s := "v1.36.3+k3s1"
	state := &clusterstate.State{Nodes: map[string]clusterstate.Node{}, Updates: clusterstate.UpdateJournal{Coordinators: map[string]clusterstate.CoordinatorReport{}}}
	target := clusterstate.Revision{Platform: clusterstate.PlatformState{Version: release}, Nodes: map[string]clusterstate.RevisionNode{}}
	for i := range 11 {
		id := fmt.Sprint(i)
		role := "agent"
		if i < 3 {
			role = "server"
		}
		target.Nodes[id] = clusterstate.RevisionNode{ID: id, Name: id, Role: role}
		state.Nodes[id] = clusterstate.Node{Phase: "active", AgentVersion: release, K3sVersion: k3s, LastSeen: now}
		if role == "server" {
			state.Updates.Coordinators[id] = clusterstate.CoordinatorReport{Version: release, LastSeen: now}
		}
	}
	require.NoError(t, verifyReleaseReports(state, target, k3s, now))
	state.Updates.Coordinators["0"] = clusterstate.CoordinatorReport{Version: "v0.1.0-alpha.4", LastSeen: now}
	require.ErrorContains(t, verifyReleaseReports(state, target, k3s, now), "coordinator")
	state.Updates.Coordinators["0"] = clusterstate.CoordinatorReport{Version: release, LastSeen: now.Add(-3 * time.Minute)}
	require.ErrorContains(t, verifyReleaseReports(state, target, k3s, now), "coordinator")
	state.Updates.Coordinators["0"] = clusterstate.CoordinatorReport{Version: release, LastSeen: now}
	node := state.Nodes["8"]
	node.LastSeen = now.Add(-3 * time.Minute)
	state.Nodes["8"] = node
	require.ErrorContains(t, verifyReleaseReports(state, target, k3s, now), "healthy report")
	node.LastSeen = now
	node.AgentVersion = "v0.1.0-alpha.4"
	state.Nodes["8"] = node
	require.ErrorContains(t, verifyReleaseReports(state, target, k3s, now), "hostd")
}

func TestUpdateSurvivesControllerLeaseHandoff(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	state, err := clusterstate.NewSeedState("mixed", clusterstate.Node{
		ID: "controller-0", InstallationID: "install", Name: "controller-0", Role: "server",
		Capabilities: layout.Capabilities, AgentVersion: "v0.1.0-alpha.4", LastSeen: now,
	}, now)
	require.NoError(t, err)
	_, err = state.EditCandidate(now, func(nodes map[string]clusterstate.RevisionNode, platform *clusterstate.PlatformState) error {
		platform.Enabled, platform.Version, platform.RegistryNode = true, "v0.1.0-alpha.4", "controller-0"
		for i := 1; i < 11; i++ {
			id, role := fmt.Sprintf("node-%02d", i), "agent"
			if i < 3 {
				role = "server"
			}
			nodes[id] = clusterstate.RevisionNode{ID: id, Name: id, Role: role, Capabilities: layout.Capabilities}
			state.Nodes[id] = clusterstate.Node{ID: id, Name: id, Role: role, Phase: "active", AgentVersion: "v0.1.0-alpha.4", LastSeen: now}
		}
		return nil
	})
	require.NoError(t, err)
	state.ConvergedRevision = state.CandidateRevision
	client := fake.NewSimpleClientset()
	store := &clusterstate.Store{Client: client}
	_, err = store.Bootstrap(ctx, state)
	require.NoError(t, err)
	first := &CoordinatorDaemon{Identity: "first-process"}
	second := &CoordinatorDaemon{Identity: "replacement-process"}
	acquired, err := first.acquireOrRenew(ctx, client)
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = store.Update(ctx, func(s *clusterstate.State) error {
		_, err := clusterstate.RequestRelease(s, "v0.1.0-alpha.5", now)
		if err != nil {
			return err
		}
		if err := clusterstate.StartNodeAction(s, "controller-0", "first-attempt", now); err != nil {
			return err
		}
		return clusterstate.CompleteNodeAction(s, "controller-0", "first-attempt", true, "", now)
	})
	require.NoError(t, err)
	acquired, err = second.acquireOrRenew(ctx, client)
	require.NoError(t, err)
	require.False(t, acquired, "a live leader cannot be displaced")
	lease, err := client.CoordinationV1().Leases(clusterstate.Namespace).Get(ctx, coordinatorLease, metav1.GetOptions{})
	require.NoError(t, err)
	expired := metav1.NewMicroTime(now.Add(-time.Minute))
	lease.Spec.RenewTime = &expired
	_, err = client.CoordinationV1().Leases(clusterstate.Namespace).Update(ctx, lease, metav1.UpdateOptions{})
	require.NoError(t, err)
	acquired, err = second.acquireOrRenew(ctx, client)
	require.NoError(t, err)
	require.True(t, acquired)
	restartedStore := &clusterstate.Store{Client: client}
	loaded, err := restartedStore.Load(ctx)
	require.NoError(t, err)
	op := loaded.Operations[loaded.CurrentOperation]
	require.True(t, clusterstate.IsReleaseOperation(loaded, op))
	require.Equal(t, clusterstate.StepComplete, op.NodeSteps["controller-0"].Phase)
	actions, err := clusterstate.RunnableNodeActions(loaded)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	require.Contains(t, actions, "node-01", "replacement continues at the next controller")
}

func TestReleaseVerificationRequiresCompletedExactPlatformRollout(t *testing.T) {
	release := "v0.1.0-alpha.5"
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Generation: 2}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "skalid", Image: version.PublishedSkalidImage(release)}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1, ReadyReplicas: 1}}
	require.NoError(t, verifyReleaseDeployment(d, release))
	d.Status.ObservedGeneration = 1
	require.Error(t, verifyReleaseDeployment(d, release))
	d.Status.ObservedGeneration = 2
	d.Status.UpdatedReplicas = 0
	require.Error(t, verifyReleaseDeployment(d, release))
	d.Status.UpdatedReplicas = 1
	d.Status.Replicas = 2
	require.Error(t, verifyReleaseDeployment(d, release))
	d.Status.Replicas = 1
	d.Spec.Template.Spec.Containers[0].Image = version.PublishedSkalidImage("v0.1.0-alpha.6")
	require.ErrorContains(t, verifyReleaseDeployment(d, release), "does not match")
}

func TestVerificationUsesJournaledPinAfterRestart(t *testing.T) {
	release, pin, now := "v0.1.0-alpha.5", "v1.36.3+k3s1", time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/nodes/worker":
			_ = json.NewEncoder(w).Encode(corev1.Node{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: pin}}})
		case "/apis/apps/v1/namespaces/skali-system/deployments/skalid":
			_ = json.NewEncoder(w).Encode(appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Generation: 1}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "skalid", Image: version.PublishedSkalidImage(release)}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}})
		default:
			t.Errorf("unexpected request after all assets were installed: %s", r.URL.Path)
			http.Error(w, "release server unavailable", http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	previous := installer.ReleaseBase
	installer.ReleaseBase = server.URL
	t.Cleanup(func() { installer.ReleaseBase = previous })
	runner := &host.Fake{FS: map[string][]byte{installer.K3sKubeconfigPath: []byte(fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: %s\ncontexts:\n- name: test\n  context:\n    cluster: test\ncurrent-context: test\n", server.URL))}}
	state := &clusterstate.State{CurrentOperation: "op", Nodes: map[string]clusterstate.Node{
		"id": {ID: "id", Name: "worker", Role: "agent", Phase: "active", LastSeen: now, AgentVersion: release, K3sVersion: pin},
	}, Updates: clusterstate.UpdateJournal{Operations: map[string]clusterstate.ReleaseUpdate{"op": {Version: release, K3s: pin}}}}
	target := clusterstate.Revision{Platform: clusterstate.PlatformState{Version: release}, Nodes: map[string]clusterstate.RevisionNode{"id": {ID: "id", Name: "worker", Role: "agent"}}}
	restarted := &CoordinatorDaemon{Runner: runner, Releases: server.Client()}
	require.NoError(t, restarted.verifyRelease(context.Background(), state, target))
}

func TestReleasePreflightChecksBothArchitecturesBeforeUpgrading(t *testing.T) {
	for _, missingAMD := range []bool{false, true} {
		t.Run(fmt.Sprint("missing-amd=", missingAMD), func(t *testing.T) {
			release := "v0.1.0-alpha.5"
			visited := map[string]bool{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "checksums.txt") {
					fmt.Fprintln(w, strings.Repeat("0", 64)+"  skali-hostd_linux_arm64")
					if !missingAMD {
						fmt.Fprintln(w, strings.Repeat("1", 64)+"  skali-hostd_linux_amd64")
					}
				} else if strings.HasSuffix(r.URL.Path, "release.json") {
					fmt.Fprintf(w, `{"version":%q,"k3s":"v1.36.3+k3s1"}`, release)
				} else if strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") {
					name := strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/")
					visited[name] = true
					arch := "arm64"
					if name == "node-09" || name == "node-10" {
						arch = "amd64"
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(corev1.Node{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"}, ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: arch}}})
				} else {
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			previous := installer.ReleaseBase
			installer.ReleaseBase = server.URL
			defer func() { installer.ReleaseBase = previous }()
			runner := &host.Fake{FS: map[string][]byte{installer.K3sKubeconfigPath: []byte(fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: %s\ncontexts:\n- name: test\n  context:\n    cluster: test\ncurrent-context: test\n", server.URL))}}
			d := &CoordinatorDaemon{Runner: runner, Releases: server.Client()}
			state := &clusterstate.State{Nodes: map[string]clusterstate.Node{}}
			target := clusterstate.Revision{Platform: clusterstate.PlatformState{Version: release}, Nodes: map[string]clusterstate.RevisionNode{}}
			for i := range 11 {
				id := fmt.Sprintf("node-%02d", i)
				target.Nodes[id] = clusterstate.RevisionNode{ID: id, Name: id}
				state.Nodes[id] = clusterstate.Node{K3sVersion: "v1.36.3+k3s1"}
			}
			err := d.upgradePreflight(context.Background(), state, target)
			if missingAMD {
				require.ErrorContains(t, err, "skali-hostd_linux_amd64")
			} else {
				require.NoError(t, err)
				require.Len(t, visited, 11)
			}
			require.Empty(t, runner.Commands, "preflight does not change any host")
		})
	}
}
