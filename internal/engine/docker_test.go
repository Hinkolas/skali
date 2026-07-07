package engine_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/engine"
)

const testImage = "alpine:3"

// testEngine connects to a real daemon or skips — the Docker analog of
// testdb.New: no build tags, tests needing an external service skip when it
// isn't there. TEST_ENGINE_SOCKET overrides the default local socket.
func testEngine(t *testing.T) *engine.Docker {
	t.Helper()
	socket := os.Getenv("TEST_ENGINE_SOCKET")
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	eng, err := engine.NewDocker(socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = eng.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := eng.List(ctx); err != nil {
		t.Skipf("skipping: no container engine at %s (%v)", socket, err)
	}
	return eng
}

// rawClient is a second, guard-rail-free client for arranging fixtures the
// engine itself refuses to touch (unmanaged containers) and for cleanup.
func rawClient(t *testing.T) *mobyclient.Client {
	t.Helper()
	socket := os.Getenv("TEST_ENGINE_SOCKET")
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	cli, err := mobyclient.New(mobyclient.WithHost(socket))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func removeOnCleanup(t *testing.T, id string) {
	t.Helper()
	cli := rawClient(t)
	t.Cleanup(func() {
		_, _ = cli.ContainerRemove(context.Background(), id, mobyclient.ContainerRemoveOptions{Force: true})
	})
}

func TestDockerLifecycle(t *testing.T) {
	eng := testEngine(t)
	ctx := t.Context()

	require.NoError(t, eng.Pull(ctx, testImage))
	exists, err := eng.ImageExists(ctx, testImage)
	require.NoError(t, err)
	require.True(t, exists)

	name := fmt.Sprintf("skali-engine-test-%d", time.Now().UnixNano())
	id, err := eng.Create(ctx, engine.ContainerSpec{
		Name:    name,
		Image:   testImage,
		Command: []string{"sleep", "300"},
		Labels:  map[string]string{engine.LabelKind: engine.KindSystem},
	})
	require.NoError(t, err)
	removeOnCleanup(t, id)

	require.NoError(t, eng.Start(ctx, id))

	c, err := eng.Inspect(ctx, id)
	require.NoError(t, err)
	require.Equal(t, name, c.Name)
	require.Equal(t, "running", c.State)
	require.Equal(t, "true", c.Labels[engine.LabelManaged])
	require.Equal(t, engine.KindSystem, c.Labels[engine.LabelKind])
	require.False(t, c.StartedAt.IsZero())
	require.Empty(t, c.Health, "no healthcheck configured")

	listed, err := eng.List(ctx)
	require.NoError(t, err)
	require.True(t, containsID(listed, id))

	raw, err := eng.Stats(ctx, id)
	require.NoError(t, err)
	require.False(t, raw.At.IsZero())
	require.NotZero(t, raw.MemLimit)

	// A second create with the same name is a conflict.
	_, err = eng.Create(ctx, engine.ContainerSpec{Name: name, Image: testImage})
	require.ErrorIs(t, err, engine.ErrConflict)

	require.NoError(t, eng.Stop(ctx, id, 2*time.Second))
	c, err = eng.Inspect(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "exited", c.State)

	require.NoError(t, eng.Remove(ctx, id, false))
	_, err = eng.Inspect(ctx, id)
	require.ErrorIs(t, err, engine.ErrNotFound)
}

func TestDockerIgnoresUnmanagedContainers(t *testing.T) {
	eng := testEngine(t)
	cli := rawClient(t)
	ctx := t.Context()

	require.NoError(t, eng.Pull(ctx, testImage))

	// An unlabeled container, created behind skali's back.
	res, err := cli.ContainerCreate(ctx, mobyclient.ContainerCreateOptions{
		Name:  fmt.Sprintf("skali-engine-unmanaged-%d", time.Now().UnixNano()),
		Image: testImage,
	})
	require.NoError(t, err)
	removeOnCleanup(t, res.ID)

	listed, err := eng.List(ctx)
	require.NoError(t, err)
	require.False(t, containsID(listed, res.ID), "unlabeled containers must be invisible")

	_, err = eng.Inspect(ctx, res.ID)
	require.ErrorIs(t, err, engine.ErrNotManaged)
	require.ErrorIs(t, eng.Start(ctx, res.ID), engine.ErrNotManaged)
	require.ErrorIs(t, eng.Remove(ctx, res.ID, true), engine.ErrNotManaged)
}

func TestDockerPullNeverFailsOnMissingImage(t *testing.T) {
	eng := testEngine(t)
	ctx := t.Context()

	_, err := engine.Deploy(ctx, eng, engine.ContainerSpec{
		Name:  fmt.Sprintf("skali-engine-noimg-%d", time.Now().UnixNano()),
		Image: "skali.invalid/does-not-exist:latest",
	}, engine.PullNever, false)
	require.ErrorIs(t, err, engine.ErrImageMissing)
}

func containsID(cs []engine.Container, id string) bool {
	for _, c := range cs {
		if c.ID == id {
			return true
		}
	}
	return false
}
