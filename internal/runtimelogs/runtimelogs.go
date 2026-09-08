// Package runtimelogs streams live application logs from the cluster
// through the public API: a merged per-environment stream with member
// labels, best-effort previous-container output after restarts, and
// automatic attachment of new members as they appear. This is a deliberate
// runtime pass-through read: runtime logs stay out of the system
// database entirely, so unlike topology projections (which never
// touch Kubernetes at request time) each log subscription holds live
// follow streams against the kubelet for exactly as long as the client
// stays attached. Deployment-step logs live in the journal and are never
// mixed in here.
package runtimelogs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	skalikube "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
)

var (
	// ErrNoCluster: skalid runs API-only; there are no runtime logs.
	ErrNoCluster = errors.New("runtimelogs: no cluster connected")
	// ErrEnvironmentNotFound mirrors the store lookup.
	ErrEnvironmentNotFound = errors.New("runtimelogs: environment not found")
)

// tailLines bounds the backlog a fresh subscription replays per member.
const tailLines = int64(100)

// reattachInterval backstops missed invalidations; new members normally
// attach on the watch-driven nudge.
const reattachInterval = 10 * time.Second

// Event is one log line of one member.
type Event struct {
	Service  string    `json:"service"`
	Pod      string    `json:"pod"`
	Line     string    `json:"line"`
	Time     time.Time `json:"time,omitzero"`
	Previous bool      `json:"previous,omitempty"`
}

type Streamer struct {
	Clientset kubernetes.Interface
	Observed  *observe.Store
	Store     *store.Store
}

// Stream follows the environment's member logs into out until ctx ends.
// service filters to one service key; empty follows every application
// member of the environment. The caller owns out and must drain it.
func (s *Streamer) Stream(ctx context.Context, environmentID uuid.UUID, service string, out chan<- Event) error {
	if s == nil || s.Clientset == nil {
		return ErrNoCluster
	}
	namespace, err := s.namespace(ctx, environmentID)
	if err != nil {
		return err
	}

	invalidations, cancelSubscription := s.Observed.Subscribe(environmentID)
	defer func() { cancelSubscription() }()

	var wg sync.WaitGroup
	defer wg.Wait()
	streamCtx, cancelStreams := context.WithCancel(ctx)
	defer cancelStreams()

	attached := make(map[string]bool)
	attach := func() {
		snapshot := s.Observed.Snapshot(environmentID)
		for _, object := range snapshot.Objects {
			if object.Kind != module.KindPod || object.Pod == nil {
				continue
			}
			if service != "" && object.Service != service {
				continue
			}
			pod := object.Ref.Name
			if attached[pod] {
				continue
			}
			attached[pod] = true
			previous := object.Pod.Restarts > 0
			serviceKey := object.Service
			wg.Go(func() {
				s.followPod(streamCtx, namespace, serviceKey, pod, previous, out)
			})
		}
	}

	attach()
	ticker := time.NewTicker(reattachInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, open := <-invalidations:
			if !open {
				// Fell behind the broadcaster; resubscribe and rescan.
				cancelSubscription()
				invalidations, cancelSubscription = s.Observed.Subscribe(environmentID)
			}
			attach()
		case <-ticker.C:
			attach()
		}
	}
}

// followPod copies one member's log stream into out: a best-effort
// previous-container tail first when the member restarted, then a live
// follow. The goroutine ends with the pod's stream (deletion) or the
// subscription context.
func (s *Streamer) followPod(ctx context.Context, namespace, service, pod string, previous bool, out chan<- Event) {
	if previous {
		s.copyLogs(ctx, namespace, service, pod, true, out)
	}
	s.copyLogs(ctx, namespace, service, pod, false, out)
}

func (s *Streamer) copyLogs(ctx context.Context, namespace, service, pod string, previous bool, out chan<- Event) {
	tail := tailLines
	options := &corev1.PodLogOptions{
		Timestamps: true,
		TailLines:  &tail,
		Previous:   previous,
		Follow:     !previous,
	}
	stream, err := s.Clientset.CoreV1().Pods(namespace).GetLogs(pod, options).Stream(ctx)
	if err != nil {
		// Racing a terminating or not-yet-started container is routine;
		// the next invalidation re-attaches when the member returns.
		return
	}
	defer stream.Close()
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		timestamp, line := splitTimestamp(scanner.Text())
		event := Event{Service: service, Pod: pod, Line: line, Time: timestamp, Previous: previous}
		select {
		case out <- event:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Streamer) namespace(ctx context.Context, environmentID uuid.UUID) (string, error) {
	env, err := s.Store.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrEnvironmentNotFound
		}
		return "", fmt.Errorf("runtimelogs: get environment: %w", err)
	}
	return skalikube.NamespaceName(env.ID.String()), nil
}

// splitTimestamp separates the kubelet's RFC3339Nano prefix from the line.
func splitTimestamp(raw string) (time.Time, string) {
	prefix, rest, found := strings.Cut(raw, " ")
	if !found {
		return time.Time{}, raw
	}
	timestamp, err := time.Parse(time.RFC3339Nano, prefix)
	if err != nil {
		return time.Time{}, raw
	}
	return timestamp, rest
}
