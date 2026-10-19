//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/build"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/compiler"
)

// cancelServer answers POST /v1/runs/{id}/cancel with the next of
// replies (the last repeats), after reporting the id on the returned
// channel, and GET /v1/runs/{id} with a run in runStatus (a server error
// when empty).
func cancelServer(t *testing.T, runStatus string, replies ...func(w http.ResponseWriter)) (*client.Client, <-chan string) {
	t.Helper()
	cancels := make(chan string, 4)
	var mu sync.Mutex
	answered := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
		if r.Method == http.MethodGet {
			if runStatus == "" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(client.RunTree{
				Run: client.Run{ID: id, Kind: "deployment", Status: runStatus}, Steps: []client.Step{}})
			return
		}
		id, ok := strings.CutSuffix(id, "/cancel")
		if r.Method != http.MethodPost || !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		cancels <- id
		mu.Lock()
		reply := replies[min(answered, len(replies)-1)]
		answered++
		mu.Unlock()
		reply(w)
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "token", client.Caller{UserAgent: "test"}), cancels
}

func cancelledReply(fallback, continues bool) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "cancelled", "fallback": fallback, "continues": continues})
	}
}

func conflictReply(message string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"code": "conflict", "message": message}})
	}
}

func receive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
		panic("unreachable")
	}
}

// watchSignals subscribes the test to SIGINT. Signals are process-wide and
// the tests sending them own them; the subscription tells a test when one
// arrived, and keeps a handler that went missing from killing the process.
func watchSignals(t *testing.T) <-chan os.Signal {
	t.Helper()
	seen := make(chan os.Signal, 4)
	signal.Notify(seen, os.Interrupt)
	t.Cleanup(func() { signal.Stop(seen) })
	return seen
}

func TestArtifactWindowInterruptCancelsTheDeployment(t *testing.T) {
	seen := watchSignals(t)
	release := make(chan struct{})
	api, cancels := cancelServer(t, "running", func(w http.ResponseWriter) {
		<-release
		cancelledReply(false, false)(w)
	})
	var out bytes.Buffer
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- watchArtifactWindow(context.Background(), true).run(&out, api, "r1", "", func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
	receive(t, seen)
	require.Equal(t, "r1", receive(t, cancels))

	// Ctrl-C again while the cancel request is in flight: the request is
	// still answered rather than abandoned, which would strand the run.
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
	receive(t, seen)
	close(release)

	require.EqualError(t, receive(t, done), "run r1 was cancelled")
	require.Contains(t, out.String(), "interrupted: cancelling run r1")
	require.Contains(t, out.String(), "The environment is unchanged")
	require.Empty(t, cancels)
}

func TestArtifactWindowInterruptDuringOpen(t *testing.T) {
	seen := watchSignals(t)
	api, cancels := cancelServer(t, "running", cancelledReply(false, false))

	// The server opens the window while the user presses Ctrl-C: the
	// answer arrives after the signal and names the run to cancel.
	opened, window, err := openArtifactWindow(context.Background(), true, func() (*client.OpenedDeployment, error) {
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
		receive(t, seen)
		return &client.OpenedDeployment{Deployment: &client.Deployment{ID: "d1", RunID: "r1"}}, nil
	})
	require.NoError(t, err)
	require.Equal(t, "r1", opened.Deployment.RunID)

	var out bytes.Buffer
	err = window.run(&out, api, "r1", "", func(ctx context.Context) error {
		// Work that starts before the signal ends the context stops with it.
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Error("the signal during the open did not end the window")
		}
		return ctx.Err()
	})
	require.EqualError(t, err, "run r1 was cancelled")
	require.Equal(t, "r1", receive(t, cancels))
}

func TestArtifactWindowInterruptDuringAnOpenThatOpensNothing(t *testing.T) {
	seen := watchSignals(t)
	failure := errors.New("reauthentication required")
	for name, answer := range map[string]func() (*client.OpenedDeployment, error){
		"up to date": func() (*client.OpenedDeployment, error) { return &client.OpenedDeployment{UpToDate: true}, nil },
		"refused":    func() (*client.OpenedDeployment, error) { return nil, failure },
	} {
		t.Run(name, func(t *testing.T) {
			// The answer arrives right behind the signal, before its delivery
			// can have ended the window's context: it must still count.
			_, window, err := openArtifactWindow(context.Background(), true, func() (*client.OpenedDeployment, error) {
				require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
				receive(t, seen)
				return answer()
			})
			require.ErrorIs(t, err, errOpenInterrupted)
			require.Nil(t, window)
		})
	}
}

func TestArtifactWindowWithoutInterrupt(t *testing.T) {
	api, cancels := cancelServer(t, "running", cancelledReply(false, false))

	t.Run("finished work is not cancelled", func(t *testing.T) {
		var out bytes.Buffer
		failure := errors.New("deployment failed")
		err := watchArtifactWindow(context.Background(), true).run(&out, api, "r1", "", func(context.Context) error {
			return failure
		})
		require.ErrorIs(t, err, failure)
		require.Empty(t, out.String())
	})

	t.Run("an ended parent is not an interrupt", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var out bytes.Buffer
		err := watchArtifactWindow(ctx, true).run(&out, api, "r1", "", func(ctx context.Context) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, out.String())
	})

	t.Run("a parent ended before the work", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for _, own := range []bool{true, false} {
			var out bytes.Buffer
			err := watchArtifactWindow(ctx, own).run(&out, api, "r1", "", func(context.Context) error {
				t.Error("work ran on an ended context")
				return nil
			})
			require.ErrorIs(t, err, context.Canceled)
		}
	})

	t.Run("an open that opens nothing", func(t *testing.T) {
		opened, window, err := openArtifactWindow(context.Background(), true, func() (*client.OpenedDeployment, error) {
			return &client.OpenedDeployment{UpToDate: true}, nil
		})
		require.NoError(t, err)
		require.True(t, opened.UpToDate)
		require.Nil(t, window)

		failure := errors.New("refused")
		_, window, err = openArtifactWindow(context.Background(), true, func() (*client.OpenedDeployment, error) {
			return nil, failure
		})
		require.ErrorIs(t, err, failure)
		require.Nil(t, window)
	})

	require.Empty(t, cancels)
}

func TestCancelInterruptedDeploymentOutcomes(t *testing.T) {
	t.Run("first deployment keeps rolling out", func(t *testing.T) {
		api, _ := cancelServer(t, "running", cancelledReply(false, true))
		var out bytes.Buffer
		err := reportWindowCancel(&out, requestWindowCancel(context.Background(), api, "r1"), "r1", "")
		require.EqualError(t, err, "run r1 was cancelled")
		require.Contains(t, out.String(), "first deployment")
		require.NotContains(t, out.String(), "unchanged")
	})

	t.Run("a promoted run falls back", func(t *testing.T) {
		api, _ := cancelServer(t, "running", cancelledReply(true, false))
		var out bytes.Buffer
		err := reportWindowCancel(&out, requestWindowCancel(context.Background(), api, "r1"), "r1", "")
		require.EqualError(t, err, "run r1 was cancelled")
		require.Contains(t, out.String(), "returns to its active revision")
	})

	t.Run("a promotion between the server's read and its cancel", func(t *testing.T) {
		api, cancels := cancelServer(t, "running",
			conflictReply("invalid deployment transition: promoted -> cancelled"), cancelledReply(true, false))
		var out bytes.Buffer
		err := reportWindowCancel(&out, requestWindowCancel(context.Background(), api, "r1"), "r1", "")
		require.EqualError(t, err, "run r1 was cancelled")
		require.Contains(t, out.String(), "returns to its active revision")
		require.Equal(t, "r1", receive(t, cancels))
		require.Equal(t, "r1", receive(t, cancels))
	})

	t.Run("a run that ended first", func(t *testing.T) {
		api, cancels := cancelServer(t, "failed", conflictReply("the run is already failed"))
		var out bytes.Buffer
		err := reportWindowCancel(&out, requestWindowCancel(context.Background(), api, "r1"), "r1", "")
		require.ErrorContains(t, err, "the run is already failed")
		require.NotContains(t, err.Error(), "skali run cancel")
		require.Equal(t, "r1", receive(t, cancels))
		require.Empty(t, cancels, "a finished run is not asked again")
	})

	t.Run("a conflict whose run cannot be read names the retry", func(t *testing.T) {
		api, cancels := cancelServer(t, "",
			conflictReply("invalid deployment transition: promoted -> cancelled"))
		var out bytes.Buffer
		err := reportWindowCancel(&out, requestWindowCancel(context.Background(), api, "r1"), "r1", "")
		require.ErrorContains(t, err, "promoted -> cancelled")
		require.ErrorContains(t, err, "retry with skali run cancel r1")
		require.Equal(t, "r1", receive(t, cancels))
		require.Empty(t, cancels)
	})

	t.Run("an unanswered cancel names the retry", func(t *testing.T) {
		api, _ := cancelServer(t, "running", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		var out bytes.Buffer
		err := reportWindowCancel(&out, requestWindowCancel(context.Background(), api, "r1"), "r1", "prod")
		require.ErrorContains(t, err, "retry with skali run cancel --remote prod r1")
	})
}

// fakeWindowAPI serves the endpoints an artifact window calls and records
// what would change the run: step statuses, failures, completions, and
// cancellations.
type fakeWindowAPI struct {
	mu        sync.Mutex
	statuses  map[string]string
	fails     []string
	completes []string
	cancels   []string
}

func newFakeWindowAPI(t *testing.T) (*fakeWindowAPI, *client.Client) {
	t.Helper()
	f := &fakeWindowAPI{statuses: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/steps"):
			var body struct{ Key string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(client.Step{ID: body.Key, Key: body.Key})
		case r.Method == http.MethodPatch && strings.HasPrefix(path, "/v1/steps/"):
			var body struct{ Status string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.statuses[strings.TrimPrefix(path, "/v1/steps/")] = body.Status
		case strings.HasSuffix(path, "/logs"):
		case strings.HasSuffix(path, "/heartbeat"):
			_ = json.NewEncoder(w).Encode(map[string]bool{"alive": true})
		case strings.HasSuffix(path, "/fail"):
			f.fails = append(f.fails, path)
		case strings.HasSuffix(path, "/complete"):
			f.completes = append(f.completes, path)
		case strings.HasSuffix(path, "/cancel"):
			f.cancels = append(f.cancels, strings.TrimSuffix(strings.TrimPrefix(path, "/v1/runs/"), "/cancel"))
			cancelledReply(false, false)(w)
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return f, client.New(srv.URL, "token", client.Caller{UserAgent: "test"})
}

// interruptArtifactWindow runs the opened deployment's actions inside the
// window while interrupt delivers one SIGINT, and returns the window's
// output and error, and a channel closed once the actions returned, which
// may be after the window ended.
func interruptArtifactWindow(t *testing.T, api *client.Client, opened *client.OpenedDeployment,
	project *localProject, contexts map[string]*build.Context, interrupt func()) (string, error, <-chan struct{}) {
	t.Helper()
	seen := watchSignals(t)
	var out syncBuffer
	done := make(chan error, 1)
	returned := make(chan struct{})
	go func() {
		done <- watchArtifactWindow(context.Background(), true).run(&out, api, opened.Deployment.RunID, "", func(ctx context.Context) error {
			defer close(returned)
			return executeActions(ctx, &out, api, opened, project, contexts, nil, false)
		})
	}()
	interrupt()
	receive(t, seen)
	err := receive(t, done)
	return out.String(), err, returned
}

// interruptOnce sends SIGINT once started says the work is under way.
func interruptOnce(t *testing.T, started <-chan struct{}) func() {
	return func() {
		receive(t, started)
		require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGINT))
	}
}

// requireCancelledNotFailed checks that the interrupt cancelled the run and
// left the journal to the server: nothing was failed or completed.
func (f *fakeWindowAPI) requireCancelledNotFailed(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, []string{"r1"}, f.cancels)
	require.Empty(t, f.fails)
	require.Empty(t, f.completes)
	for step, status := range f.statuses {
		require.NotEqual(t, "failed", status, "step %s", step)
	}
}

// buildWindow puts a docker running script first on PATH and returns a
// window that builds one application with it.
func buildWindow(t *testing.T, script string) (*client.OpenedDeployment, *localProject, map[string]*build.Context) {
	t.Helper()
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\n"+script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	project := &localProject{Root: root, Result: &compiler.Result{Definition: compiler.ProjectDefinition{
		Applications: map[string]compiler.Application{"web": {Source: compiler.ApplicationSource{
			Kind: "build", Build: compiler.Build{Context: ".", Dockerfile: "Dockerfile"}}}},
	}}}
	opened := &client.OpenedDeployment{
		Deployment: &client.Deployment{ID: "d1", RunID: "r1"},
		Actions: []client.ArtifactAction{{Application: "web", Action: "build", ArtifactID: "a1",
			BuildID: "b1", Platform: "linux/arm64", PushRef: "127.0.0.1:1/web:a1", StepKey: "artifacts.web"}},
	}
	return opened, project, map[string]*build.Context{"web": {Dir: root}}
}

func TestArtifactWindowInterruptDuringBuild(t *testing.T) {
	// A docker that starts the build and never finishes it: exec keeps the
	// pid, so cancelling the command closes its output.
	marker := filepath.Join(t.TempDir(), "building")
	t.Setenv("SKALI_TEST_BUILD_MARKER", marker)
	opened, project, contexts := buildWindow(t, "touch \"$SKALI_TEST_BUILD_MARKER\"\nexec sleep 30\n")
	f, api := newFakeWindowAPI(t)
	started := make(chan struct{})
	go func() {
		for {
			if _, err := os.Stat(marker); err == nil {
				close(started)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	begun := time.Now()
	out, err, _ := interruptArtifactWindow(t, api, opened, project, contexts, interruptOnce(t, started))
	require.EqualError(t, err, "run r1 was cancelled")
	require.Less(t, time.Since(begun), 10*time.Second, "the build was not stopped")
	require.Contains(t, out, "stop  Build web (linux/arm64)")
	require.Contains(t, out, "interrupted: cancelling run r1")
	f.requireCancelledNotFailed(t)
}

func TestArtifactWindowCancelsWithoutWaitingForTheBuild(t *testing.T) {
	// A docker whose child keeps the build output open after docker itself
	// is killed, so the build cannot return: the cancel must still go out,
	// and the window must end after its wind-down.
	wind := windowWindDown
	windowWindDown = 200 * time.Millisecond
	t.Cleanup(func() { windowWindDown = wind })
	marker := filepath.Join(t.TempDir(), "building")
	pidFile := filepath.Join(t.TempDir(), "child")
	t.Setenv("SKALI_TEST_BUILD_MARKER", marker)
	t.Setenv("SKALI_TEST_CHILD_PID", pidFile)
	opened, project, contexts := buildWindow(t,
		"sleep 30 &\necho $! > \"$SKALI_TEST_CHILD_PID\"\ntouch \"$SKALI_TEST_BUILD_MARKER\"\nwait\n")
	f, api := newFakeWindowAPI(t)
	started := make(chan struct{})
	go func() {
		for {
			if _, err := os.Stat(marker); err == nil {
				close(started)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	begun := time.Now()
	_, err, returned := interruptArtifactWindow(t, api, opened, project, contexts, interruptOnce(t, started))
	require.EqualError(t, err, "run r1 was cancelled")
	require.Less(t, time.Since(begun), 10*time.Second)
	f.requireCancelledNotFailed(t)

	// The build returns once the child lets its output go; the next test
	// must not share the process with it.
	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
	receive(t, returned)
}

func TestArtifactWindowInterruptAfterDockerDiedOfIt(t *testing.T) {
	// Ctrl-C reaches docker too, and docker may die of it before this
	// process has seen the signal: here the signal lands 50ms after the
	// build failed. The grace is widened so a slow machine cannot lose it.
	grace := buildInterruptGrace
	buildInterruptGrace = 5 * time.Second
	t.Cleanup(func() { buildInterruptGrace = grace })
	opened, project, contexts := buildWindow(t, "(sleep 0.05; kill -INT $PPID) >/dev/null 2>&1 &\nexit 130\n")
	f, api := newFakeWindowAPI(t)

	out, err, _ := interruptArtifactWindow(t, api, opened, project, contexts, func() {})
	require.EqualError(t, err, "run r1 was cancelled")
	require.Contains(t, out, "stop  Build web (linux/arm64)")
	f.requireCancelledNotFailed(t)
}

func TestArtifactWindowInterruptDuringUpload(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	quiet := registry.Logger(log.New(io.Discard, "", 0))
	upstream := httptest.NewServer(registry.New(quiet))
	t.Cleanup(upstream.Close)
	image, err := random.Image(1024, 1)
	require.NoError(t, err)
	source := strings.TrimPrefix(upstream.URL, "http://") + "/library/app:1"
	sourceRef, err := name.ParseReference(source)
	require.NoError(t, err)
	require.NoError(t, remote.Write(sourceRef, image))

	// The managed registry takes the upload and never finishes it.
	uploading := make(chan struct{})
	released := make(chan struct{})
	var once sync.Once
	target := registry.New(quiet)
	managed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/blobs/uploads/") && (r.Method == http.MethodPatch || r.Method == http.MethodPut) {
			once.Do(func() { close(uploading) })
			// The server notices the client leaving only once the body is
			// read; the release covers a body that never ends.
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-released:
			}
			return
		}
		target.ServeHTTP(w, r)
	}))
	t.Cleanup(managed.Close)
	t.Cleanup(func() { close(released) })

	f, api := newFakeWindowAPI(t)
	opened := &client.OpenedDeployment{
		Deployment: &client.Deployment{ID: "d1", RunID: "r1"},
		Actions: []client.ArtifactAction{{Application: "app", Action: "import", ArtifactID: "a1",
			Upstream: source, PushRef: strings.TrimPrefix(managed.URL, "http://") + "/app:a1", StepKey: "artifacts.app"}},
	}
	out, err, _ := interruptArtifactWindow(t, api, opened, &localProject{}, nil, interruptOnce(t, uploading))
	require.EqualError(t, err, "run r1 was cancelled")
	require.Contains(t, out, "stop  Import "+source)
	f.requireCancelledNotFailed(t)
}
