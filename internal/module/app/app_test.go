package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/module"
)

func decode(t *testing.T) module.Service {
	t.Helper()
	definition := compiler.ProjectDefinition{
		Applications: map[string]compiler.Application{
			"web": {Source: compiler.ApplicationSource{Kind: "image", Image: "example.invalid/web:1"}},
		},
	}
	svc, err := Module{}.Decode(definition, "web")
	require.NoError(t, err)
	return svc
}

func freshSource() module.ObservedResource {
	return module.ObservedResource{
		Kind: module.KindSource, Name: "kubernetes",
		Source: &module.SourceStatus{State: module.SourceFresh, LastSync: time.Now()},
	}
}

func workload(desired, ready, updated int32, conditions ...module.Condition) module.ObservedResource {
	return module.ObservedResource{
		Kind: module.KindWorkload, Name: "web",
		Workload: &module.WorkloadStatus{
			Desired: desired, Ready: ready, Updated: updated, Available: ready,
			Generation: 3, ObservedGeneration: 3, Conditions: conditions,
		},
	}
}

func pod(name string, ready bool, reason string, restarts int32) module.ObservedResource {
	return module.ObservedResource{
		Kind: module.KindPod, Name: name,
		Pod: &module.PodStatus{Phase: "Running", Ready: ready, Reason: reason, Restarts: restarts},
	}
}

func TestEvaluateStaleSourceIsUnknown(t *testing.T) {
	t.Parallel()
	svc := decode(t)
	evaluation := svc.Evaluate([]module.ObservedResource{{
		Kind: module.KindSource, Name: "kubernetes",
		Source: &module.SourceStatus{State: module.SourceStale, StaleSince: time.Now()},
	}, workload(3, 3, 3)})
	require.Equal(t, module.HealthUnknown, evaluation.Health)
	require.Equal(t, "observation_stale_since", evaluation.Diagnostics[0].Code)
}

func TestEvaluateHealthy(t *testing.T) {
	t.Parallel()
	svc := decode(t)
	evaluation := svc.Evaluate([]module.ObservedResource{
		freshSource(), workload(3, 3, 3),
		pod("web-a", true, "", 0), pod("web-b", true, "", 0), pod("web-c", true, "", 0),
	})
	require.Equal(t, module.HealthHealthy, evaluation.Health)
	require.Empty(t, evaluation.Diagnostics)
}

// The exit-criterion shape: one of three members killed, health drops to
// 2/3 and the replacement member is named with its reason.
func TestEvaluateDegradedNamesMember(t *testing.T) {
	t.Parallel()
	svc := decode(t)
	evaluation := svc.Evaluate([]module.ObservedResource{
		freshSource(), workload(3, 2, 3),
		pod("web-a", true, "", 0), pod("web-b", true, "", 0),
		pod("web-c", false, "CrashLoopBackOff", 3),
	})
	require.Equal(t, module.HealthDegraded, evaluation.Health)
	require.Equal(t, "partial-availability", evaluation.Diagnostics[0].Code)
	require.Equal(t, "2/3 members ready", evaluation.Diagnostics[0].Message)
	require.Len(t, evaluation.Diagnostics, 2)
	require.Equal(t, "member-not-ready", evaluation.Diagnostics[1].Code)
	require.Equal(t, "error", evaluation.Diagnostics[1].Severity)
	require.Equal(t, "web-c: CrashLoopBackOff, 3 restarts", evaluation.Diagnostics[1].Message)
}

func TestEvaluateUnhealthyAtZeroReady(t *testing.T) {
	t.Parallel()
	svc := decode(t)
	evaluation := svc.Evaluate([]module.ObservedResource{
		freshSource(), workload(3, 0, 3),
		pod("web-a", false, "ImagePullBackOff", 0),
	})
	require.Equal(t, module.HealthUnhealthy, evaluation.Health)
	require.Equal(t, "no-ready-replicas", evaluation.Diagnostics[0].Code)
	require.Equal(t, "0/3 members ready", evaluation.Diagnostics[0].Message)
}

func TestEvaluateAutoscalerOwnedDesire(t *testing.T) {
	t.Parallel()
	svc := decode(t)
	evaluation := svc.Evaluate([]module.ObservedResource{
		freshSource(), workload(-1, 4, 4),
		{Kind: module.KindAutoscaler, Name: "web",
			Autoscaler: &module.AutoscalerStatus{Min: 1, Max: 5, Current: 4, DesiredReplicas: 4}},
	})
	require.Equal(t, module.HealthHealthy, evaluation.Health)
}

func TestEvaluateProgressing(t *testing.T) {
	t.Parallel()
	svc := decode(t)

	// Controller lag: the latest generation is unobserved.
	lagging := workload(3, 3, 3)
	lagging.Workload.ObservedGeneration = 2
	evaluation := svc.Evaluate([]module.ObservedResource{freshSource(), lagging})
	require.Equal(t, module.HealthProgressing, evaluation.Health)
	require.Equal(t, "rollout-pending", evaluation.Diagnostics[0].Code)

	// Rolling update: members not yet replaced, even while ready count
	// still satisfies the desire through surge.
	evaluation = svc.Evaluate([]module.ObservedResource{freshSource(), workload(3, 3, 1)})
	require.Equal(t, module.HealthProgressing, evaluation.Health)
	require.Equal(t, "rolling-update", evaluation.Diagnostics[0].Code)
	require.Equal(t, "1/3 members updated", evaluation.Diagnostics[0].Message)
}

// An exceeded progress deadline must never read as progressing: the
// controller has given up and the state is degraded or unhealthy.
func TestEvaluateProgressDeadlineExceeded(t *testing.T) {
	t.Parallel()
	svc := decode(t)
	exceeded := module.Condition{
		Type: "Progressing", Status: "False",
		Reason: "ProgressDeadlineExceeded", Message: "ReplicaSet web-6d9f7b has timed out progressing",
	}
	evaluation := svc.Evaluate([]module.ObservedResource{
		freshSource(), workload(3, 1, 1, exceeded),
		pod("web-a", false, "CrashLoopBackOff", 7),
	})
	require.Equal(t, module.HealthDegraded, evaluation.Health)
	codes := make([]string, 0, len(evaluation.Diagnostics))
	for _, diagnostic := range evaluation.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	require.Contains(t, codes, "progress-deadline-exceeded")

	evaluation = svc.Evaluate([]module.ObservedResource{freshSource(), workload(3, 0, 1, exceeded)})
	require.Equal(t, module.HealthUnhealthy, evaluation.Health)
}

func TestEvaluateUnknownCases(t *testing.T) {
	t.Parallel()
	svc := decode(t)

	evaluation := svc.Evaluate([]module.ObservedResource{freshSource()})
	require.Equal(t, module.HealthUnknown, evaluation.Health)
	require.Equal(t, "missing-resource", evaluation.Diagnostics[0].Code)

	evaluation = svc.Evaluate([]module.ObservedResource{freshSource(), workload(0, 0, 0)})
	require.Equal(t, module.HealthUnknown, evaluation.Health)
	require.Equal(t, "no-replicas", evaluation.Diagnostics[0].Code)
}

func TestDecodeAndRemoval(t *testing.T) {
	t.Parallel()
	definition := compiler.ProjectDefinition{
		Applications: map[string]compiler.Application{
			"web": {
				Source:  compiler.ApplicationSource{Kind: "image", Image: "example.invalid/web:1"},
				Volumes: map[string]compiler.Volume{"data": {MountPath: "/data"}},
			},
		},
		Dependencies: map[string][]string{"applications.web": {"databases.data"}},
	}
	svc, err := Module{}.Decode(definition, "web")
	require.NoError(t, err)
	require.Equal(t, "web", svc.Key())
	require.Equal(t, []string{"databases.data"}, svc.Dependencies())
	require.True(t, svc.Removal().DataLoss)

	_, err = Module{}.Decode(definition, "missing")
	require.Error(t, err)
}
