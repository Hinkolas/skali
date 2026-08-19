package podexec

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

func TestResolveGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var absent *Service
	_, err := absent.Resolve(ctx, uuid.New(), Options{Service: "web"})
	require.ErrorIs(t, err, ErrNoCluster)

	apiOnly := &Service{}
	_, err = apiOnly.Resolve(ctx, uuid.New(), Options{Service: "web"})
	require.ErrorIs(t, err, ErrNoCluster)

	st := store.NewStore(testdb.New(t))
	service := &Service{Kube: &kube.Client{}, Observed: observe.NewFake().Store, Store: st}

	_, err = service.Resolve(ctx, uuid.New(), Options{})
	require.ErrorContains(t, err, "service or pod required")

	_, err = service.Resolve(ctx, uuid.New(), Options{Service: "Not,Valid=Key"})
	require.ErrorContains(t, err, "invalid service")

	_, err = service.Resolve(ctx, uuid.New(), Options{Service: "web"})
	require.ErrorIs(t, err, ErrEnvironmentNotFound)
}

// testEnv creates a project/environment pair and a resolver over a fake
// observed store, returning both for the pod-population steps of each test.
func testEnv(t *testing.T) (*Service, *observe.Fake, uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	st := store.NewStore(testdb.New(t))
	projects := project.New(st)
	proj, err := projects.Create(ctx, "demo", "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)

	observed := observe.NewFake()
	observed.SetFresh()
	service := &Service{Kube: &kube.Client{}, Observed: observed.Store, Store: st}
	return service, observed, env.ID, "skali-demo-production"
}

func TestResolvePicksNewestReadyPod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, observed, envID, namespace := testEnv(t)

	started := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	observed.SetPod(envID, namespace, "web", "web-old", "node-a",
		module.PodStatus{Phase: "Running", Ready: true, Started: started})
	observed.SetPod(envID, namespace, "web", "web-new", "node-a",
		module.PodStatus{Phase: "Running", Ready: true, Started: started.Add(time.Minute)})
	observed.SetPod(envID, namespace, "web", "web-unready", "node-a",
		module.PodStatus{Phase: "Running", Ready: false, Started: started.Add(time.Hour)})
	observed.SetPod(envID, namespace, "worker", "worker-1", "node-a",
		module.PodStatus{Phase: "Running", Ready: true, Started: started.Add(time.Hour)})

	session, err := service.Resolve(ctx, envID, Options{Service: "web", TTY: true})
	require.NoError(t, err)
	require.Equal(t, namespace, session.Namespace)
	require.Equal(t, "web-new", session.Pod)
	require.Equal(t, "web", session.Container, "container defaults to the service key")
	require.Equal(t, []string{"/bin/sh"}, session.Command, "command defaults to a shell")
	require.True(t, session.TTY)
}

func TestResolveNameTieBreak(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, observed, envID, namespace := testEnv(t)

	started := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	observed.SetPod(envID, namespace, "web", "web-b", "node-a",
		module.PodStatus{Phase: "Running", Ready: true, Started: started})
	observed.SetPod(envID, namespace, "web", "web-a", "node-a",
		module.PodStatus{Phase: "Running", Ready: true, Started: started})

	session, err := service.Resolve(ctx, envID, Options{Service: "web"})
	require.NoError(t, err)
	require.Equal(t, "web-a", session.Pod)
}

func TestResolveNoReadyPod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, observed, envID, namespace := testEnv(t)

	observed.SetPod(envID, namespace, "web", "web-1", "node-a",
		module.PodStatus{Phase: "Pending", Ready: false, Reason: "ImagePullBackOff"})

	_, err := service.Resolve(ctx, envID, Options{Service: "web"})
	var noReady *NoReadyPodError
	require.ErrorAs(t, err, &noReady)
	require.Equal(t, "web", noReady.Service)
	require.Len(t, noReady.Pods, 1)
	require.Equal(t, "web-1", noReady.Pods[0].Name)
	require.Contains(t, noReady.Error(), "ImagePullBackOff")

	// No pods at all reads the same way.
	_, err = service.Resolve(ctx, envID, Options{Service: "api"})
	require.ErrorAs(t, err, &noReady)
	require.Empty(t, noReady.Pods)
}

func TestResolveExplicitPod(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, observed, envID, namespace := testEnv(t)

	observed.SetPod(envID, namespace, "web", "web-crashing", "node-a",
		module.PodStatus{Phase: "Running", Ready: false, Reason: "CrashLoopBackOff"})
	observed.SetPod(envID, namespace, "web", "web-pending", "node-a",
		module.PodStatus{Phase: "Pending", Ready: false})

	// Running but unready is allowed: crash-looping containers stay
	// debuggable through an explicit pod.
	session, err := service.Resolve(ctx, envID, Options{Service: "web", Pod: "web-crashing"})
	require.NoError(t, err)
	require.Equal(t, "web-crashing", session.Pod)

	_, err = service.Resolve(ctx, envID, Options{Service: "web", Pod: "web-pending"})
	require.ErrorIs(t, err, ErrPodNotFound)

	_, err = service.Resolve(ctx, envID, Options{Service: "web", Pod: "no-such-pod"})
	require.ErrorIs(t, err, ErrPodNotFound)

	// A pod of another service is not reachable through the wrong key.
	observed.SetPod(envID, namespace, "worker", "worker-1", "node-a",
		module.PodStatus{Phase: "Running", Ready: true})
	_, err = service.Resolve(ctx, envID, Options{Service: "web", Pod: "worker-1"})
	require.ErrorIs(t, err, ErrPodNotFound)
}

func TestResolveFencesNamespace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, observed, envID, _ := testEnv(t)

	// An object that leaked into the snapshot from another namespace is
	// never an exec target, whatever its labels claim.
	observed.SetPod(envID, "skali-platform", "web", "intruder", "node-a",
		module.PodStatus{Phase: "Running", Ready: true})

	_, err := service.Resolve(ctx, envID, Options{Service: "web"})
	var noReady *NoReadyPodError
	require.ErrorAs(t, err, &noReady)
	require.Empty(t, noReady.Pods)
}

func TestResolveOverrides(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, observed, envID, namespace := testEnv(t)

	observed.SetPod(envID, namespace, "web", "web-1", "node-a",
		module.PodStatus{Phase: "Running", Ready: true})

	session, err := service.Resolve(ctx, envID, Options{
		Service:   "web",
		Container: "sidecar",
		Command:   []string{"bun", "run", "seed:demo"},
	})
	require.NoError(t, err)
	require.Equal(t, "sidecar", session.Container)
	require.Equal(t, []string{"bun", "run", "seed:demo"}, session.Command)
	require.False(t, session.TTY)
}
