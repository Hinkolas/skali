// Package podexec resolves and runs interactive exec sessions in app pods
// through the public API. Like runtime logs (section 9.3) this is a
// sanctioned request-time pass-through read (section 7.4): the session
// holds a live exec stream against the kubelet for exactly as long as the
// client stays attached, and nothing is persisted. Targets resolve only
// through the environment's own namespace and the observed store's app
// pods, so platform substrate and system pods are unreachable by
// construction.
package podexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/Hinkolas/skali/internal/kube"
	skalikube "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/naming"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
)

var (
	// ErrNoCluster: skalid runs API-only; there is nothing to exec into.
	ErrNoCluster = errors.New("podexec: no cluster connected")
	// ErrEnvironmentNotFound mirrors the store lookup.
	ErrEnvironmentNotFound = errors.New("podexec: environment not found")
	// ErrPodNotFound: the requested pod is not an app pod of the
	// environment (or of the requested service).
	ErrPodNotFound = errors.New("podexec: pod not found")
	// ErrInvalidOptions: the request itself is malformed (no target, or a
	// service key outside the identifier grammar).
	ErrInvalidOptions = errors.New("podexec: invalid options")
)

// PodState describes one candidate pod in a NoReadyPodError.
type PodState struct {
	Name   string
	Phase  string
	Ready  bool
	Reason string
}

// NoReadyPodError reports that the service has no ready pod to exec into,
// listing the current members so the message is actionable.
type NoReadyPodError struct {
	Service string
	Pods    []PodState
}

func (e *NoReadyPodError) Error() string {
	if len(e.Pods) == 0 {
		return fmt.Sprintf("no running pod for service %q", e.Service)
	}
	states := make([]string, 0, len(e.Pods))
	for _, pod := range e.Pods {
		state := pod.Phase
		if pod.Reason != "" {
			state = pod.Reason
		}
		states = append(states, fmt.Sprintf("%s (%s)", pod.Name, state))
	}
	return fmt.Sprintf("no ready pod for service %q: %s", e.Service, strings.Join(states, ", "))
}

// defaultCommand is what a session runs when the client names no command:
// an interactive shell. Images without a shell fail with a clean
// exec_failed passthrough from the kubelet.
var defaultCommand = []string{"/bin/sh"}

// Service resolves and streams exec sessions. Kube is nil in API-only mode.
type Service struct {
	Kube     *kube.Client
	Observed *observe.Store
	Store    *store.Store
}

// Options is one client's requested exec target.
type Options struct {
	Service   string   // app service key; required unless Pod is given
	Pod       string   // explicit pod override
	Container string   // default: the service key (single-container contract)
	Command   []string // default: an interactive shell
	TTY       bool
}

// Session is a fully validated exec target, resolved before any stream is
// established.
type Session struct {
	Namespace string
	Pod       string
	Container string
	Command   []string
	TTY       bool
}

// Streams carries the live ends of one session.
type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Resize remotecommand.TerminalSizeQueue
}

// Resolve validates the target and picks the pod: the newest ready pod of
// the service by default, or the explicitly requested one. Resolution only
// consults the observed store; no request-time LIST is issued.
func (s *Service) Resolve(ctx context.Context, environmentID uuid.UUID, opts Options) (*Session, error) {
	if s == nil || s.Kube == nil {
		return nil, ErrNoCluster
	}
	if opts.Service == "" && opts.Pod == "" {
		return nil, fmt.Errorf("%w: service or pod required", ErrInvalidOptions)
	}
	if opts.Service != "" {
		// Keys interpolate into selectors and container names; reject
		// anything outside the identifier grammar outright.
		if err := naming.CheckKey(opts.Service); err != nil {
			return nil, fmt.Errorf("%w: invalid service: %v", ErrInvalidOptions, err)
		}
	}

	namespace, err := s.namespace(ctx, environmentID)
	if err != nil {
		return nil, err
	}

	var candidates []observe.Object
	snapshot := s.Observed.Snapshot(environmentID)
	for _, object := range snapshot.Objects {
		if object.Kind != module.KindPod || object.Pod == nil {
			continue
		}
		if object.Ref.Namespace != namespace {
			// Snapshots may include shared platform objects; exec stays
			// fenced to the environment's own namespace.
			continue
		}
		if opts.Service != "" && object.Service != opts.Service {
			continue
		}
		candidates = append(candidates, object)
	}

	target, err := pickPod(candidates, opts)
	if err != nil {
		return nil, err
	}

	container := opts.Container
	if container == "" {
		container = target.Service
	}
	command := opts.Command
	if len(command) == 0 {
		command = defaultCommand
	}
	return &Session{
		Namespace: namespace,
		Pod:       target.Ref.Name,
		Container: container,
		Command:   command,
		TTY:       opts.TTY,
	}, nil
}

// Stream runs the session over the given streams until the remote process
// ends or ctx is cancelled. A nonzero remote exit surfaces as
// *exec.CodeExitError, passed through from the kube layer.
func (s *Service) Stream(ctx context.Context, session *Session, streams Streams) error {
	return s.Kube.ExecStream(ctx, session.Namespace, session.Pod, session.Container, session.Command, kube.ExecStreamOptions{
		Stdin:  streams.Stdin,
		Stdout: streams.Stdout,
		Stderr: streams.Stderr,
		TTY:    session.TTY,
		Resize: streams.Resize,
	})
}

// pickPod chooses the exec target among the service's observed pods: the
// explicitly requested pod (which must be Running, though not necessarily
// Ready, so crash-looping containers stay debuggable), or the newest ready
// pod with a name tie-break for determinism.
func pickPod(candidates []observe.Object, opts Options) (*observe.Object, error) {
	if opts.Pod != "" {
		for i := range candidates {
			object := &candidates[i]
			if object.Ref.Name != opts.Pod {
				continue
			}
			if object.Pod.Phase != "Running" {
				return nil, fmt.Errorf("%w: pod %s is %s", ErrPodNotFound, opts.Pod, object.Pod.Phase)
			}
			return object, nil
		}
		return nil, fmt.Errorf("%w: %s", ErrPodNotFound, opts.Pod)
	}

	var target *observe.Object
	for i := range candidates {
		object := &candidates[i]
		if !object.Pod.Ready {
			continue
		}
		if target == nil ||
			object.Pod.Started.After(target.Pod.Started) ||
			(object.Pod.Started.Equal(target.Pod.Started) && object.Ref.Name < target.Ref.Name) {
			target = object
		}
	}
	if target == nil {
		failure := &NoReadyPodError{Service: opts.Service}
		for _, object := range candidates {
			failure.Pods = append(failure.Pods, PodState{
				Name:   object.Ref.Name,
				Phase:  object.Pod.Phase,
				Ready:  object.Pod.Ready,
				Reason: object.Pod.Reason,
			})
		}
		return nil, failure
	}
	return target, nil
}

func (s *Service) namespace(ctx context.Context, environmentID uuid.UUID) (string, error) {
	env, err := s.Store.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrEnvironmentNotFound
		}
		return "", fmt.Errorf("podexec: get environment: %w", err)
	}
	project, err := s.Store.GetProjectByID(ctx, env.ProjectID)
	if err != nil {
		return "", fmt.Errorf("podexec: get project: %w", err)
	}
	return skalikube.NamespaceName(project.Name, env.Name), nil
}
