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

// decodeRoutes builds a TLS-capable service whose application declares one
// automatic route; the expected certificate name is proj-web-public-tls.
func decodeRoutes(t *testing.T, certificates bool, tls string) module.Service {
	t.Helper()
	definition := compiler.ProjectDefinition{
		Name: "proj",
		Applications: map[string]compiler.Application{
			"web": {
				Source: compiler.ApplicationSource{Kind: "image", Image: "example.invalid/web:1"},
				Routes: map[string]compiler.Route{"public": {
					Path: "/", TLS: tls, Strategy: "round-robin",
				}},
			},
		},
	}
	svc, err := Module{Certificates: certificates}.Decode(definition, "web")
	require.NoError(t, err)
	return svc
}

func certificate(status module.CertificateStatus) module.ObservedResource {
	return module.ObservedResource{
		Kind: module.KindCertificate, Name: "tls-proj-web-public-0f0cfcc88276f2165665ed0173565bcc",
		Certificate: &status,
	}
}

func TestEvaluateCertificateGate(t *testing.T) {
	t.Parallel()
	healthyWorkload := []module.ObservedResource{freshSource(), workload(1, 1, 1)}

	t.Run("unobserved certificate keeps the service progressing", func(t *testing.T) {
		t.Parallel()
		evaluation := decodeRoutes(t, true, "automatic").Evaluate(healthyWorkload)
		require.Equal(t, module.HealthProgressing, evaluation.Health)
		require.Equal(t, "certificate-unobserved", evaluation.Diagnostics[0].Code,
			"the -unobserved suffix drives the kernel's watch refresh")
	})

	t.Run("pending issuance blocks activation and leads the diagnostics", func(t *testing.T) {
		t.Parallel()
		evaluation := decodeRoutes(t, true, "automatic").Evaluate(append(healthyWorkload,
			certificate(module.CertificateStatus{Issuing: true, Reason: "Pending",
				Message: "waiting for the ACME challenge"})))
		require.Equal(t, module.HealthProgressing, evaluation.Health)
		require.Equal(t, "certificate-pending", evaluation.Diagnostics[0].Code)
		require.Contains(t, evaluation.Diagnostics[0].Message, "waiting for the ACME challenge",
			"the failed verify step surfaces exactly this message")
	})

	t.Run("failed attempts become an error while still progressing", func(t *testing.T) {
		t.Parallel()
		evaluation := decodeRoutes(t, true, "automatic").Evaluate(append(healthyWorkload,
			certificate(module.CertificateStatus{FailedAttempts: 2, Reason: "Failed",
				Message: "ACME authorization failed"})))
		require.Equal(t, module.HealthProgressing, evaluation.Health)
		require.Equal(t, "certificate-failing", evaluation.Diagnostics[0].Code)
		require.Equal(t, "error", evaluation.Diagnostics[0].Severity)
	})

	t.Run("issued certificate leaves health untouched", func(t *testing.T) {
		t.Parallel()
		evaluation := decodeRoutes(t, true, "automatic").Evaluate(append(healthyWorkload,
			certificate(module.CertificateStatus{Ready: true,
				NotAfter: time.Now().Add(60 * 24 * time.Hour)})))
		require.Equal(t, module.HealthHealthy, evaluation.Health)
		require.Empty(t, evaluation.Diagnostics)
	})

	t.Run("failing renewal warns without blocking", func(t *testing.T) {
		t.Parallel()
		evaluation := decodeRoutes(t, true, "automatic").Evaluate(append(healthyWorkload,
			certificate(module.CertificateStatus{Ready: false, Reason: "Failed",
				NotAfter: time.Now().Add(10 * 24 * time.Hour)})))
		require.Equal(t, module.HealthHealthy, evaluation.Health,
			"a valid certificate with a failing renewal must never block a deploy")
		require.Equal(t, "certificate-renewal-failing", evaluation.Diagnostics[0].Code)
		require.Equal(t, "warning", evaluation.Diagnostics[0].Severity)
	})

	t.Run("expired certificate degrades the service", func(t *testing.T) {
		t.Parallel()
		evaluation := decodeRoutes(t, true, "automatic").Evaluate(append(healthyWorkload,
			certificate(module.CertificateStatus{Ready: false,
				NotAfter: time.Now().Add(-time.Hour)})))
		require.Equal(t, module.HealthDegraded, evaluation.Health)
		require.Equal(t, "certificate-expired", evaluation.Diagnostics[0].Code)
	})

	t.Run("workload trouble keeps its own story first", func(t *testing.T) {
		t.Parallel()
		evaluation := decodeRoutes(t, true, "automatic").Evaluate([]module.ObservedResource{
			freshSource(), workload(1, 1, 0),
		})
		require.Equal(t, module.HealthProgressing, evaluation.Health)
		require.Equal(t, "rolling-update", evaluation.Diagnostics[0].Code,
			"the certificate only leads when it is what changed the verdict")
	})

	t.Run("disabled routes and local installations ignore certificates", func(t *testing.T) {
		t.Parallel()
		evaluation := decodeRoutes(t, true, "disabled").Evaluate(healthyWorkload)
		require.Equal(t, module.HealthHealthy, evaluation.Health)
		require.Empty(t, evaluation.Diagnostics)

		evaluation = decodeRoutes(t, false, "automatic").Evaluate(healthyWorkload)
		require.Equal(t, module.HealthHealthy, evaluation.Health)
		require.Empty(t, evaluation.Diagnostics)
	})
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

func coloredWorkload(color string, desired, ready, updated int32, conditions ...module.Condition) module.ObservedResource {
	resource := workload(desired, ready, updated, conditions...)
	resource.Color = color
	return resource
}

func coloredPod(name, color string, ready bool, reason string) module.ObservedResource {
	resource := pod(name, ready, reason, 0)
	resource.Color = color
	return resource
}

func rollout(desired, serving string) module.ObservedResource {
	return module.ObservedResource{
		Kind: module.KindRollout, Name: "web",
		Rollout: &module.RolloutStatus{DesiredColor: desired, ServingColor: serving},
	}
}

func codesOf(evaluation module.Evaluation) []string {
	codes := make([]string, 0, len(evaluation.Diagnostics))
	for _, diagnostic := range evaluation.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	return codes
}

// Blue-green: the verdict follows the desired color alone. While the
// Service still selects the previous color the service is progressing (new
// members starting, or ready and about to take traffic), never healthy, so
// activation waits for the switch to land.
func TestEvaluateBlueGreenPendingAndSwitching(t *testing.T) {
	t.Parallel()
	svc := decode(t)

	// The new color is not observed yet while the old one serves.
	evaluation := svc.Evaluate([]module.ObservedResource{
		freshSource(), rollout("new", "old"), coloredWorkload("old", 2, 2, 2),
	})
	require.Equal(t, module.HealthProgressing, evaluation.Health)
	require.Equal(t, "color-pending", evaluation.Diagnostics[0].Code)

	// Starting: one of two members ready; the old color's members are not
	// counted against it.
	evaluation = svc.Evaluate([]module.ObservedResource{
		freshSource(), rollout("new", "old"),
		coloredWorkload("old", 2, 2, 2), coloredWorkload("new", 2, 1, 2),
		coloredPod("web-old-a", "old", true, ""), coloredPod("web-old-b", "old", true, ""),
		coloredPod("web-new-a", "new", true, ""), coloredPod("web-new-b", "new", false, "ContainerCreating"),
	})
	require.Equal(t, module.HealthProgressing, evaluation.Health)
	require.Equal(t, "color-pending", evaluation.Diagnostics[0].Code)
	require.Equal(t, "starting 2 new replicas (1/2 ready)", evaluation.Diagnostics[0].Message)
	require.Contains(t, codesOf(evaluation), "member-not-ready")

	// Fully ready but traffic has not moved: still progressing, switching.
	evaluation = svc.Evaluate([]module.ObservedResource{
		freshSource(), rollout("new", "old"),
		coloredWorkload("old", 2, 2, 2), coloredWorkload("new", 2, 2, 2),
	})
	require.Equal(t, module.HealthProgressing, evaluation.Health)
	require.Equal(t, "switching-traffic", evaluation.Diagnostics[0].Code)

	// The switch landed: healthy, and the retiring color's members (already
	// terminating) never degrade the verdict.
	evaluation = svc.Evaluate([]module.ObservedResource{
		freshSource(), rollout("new", "new"),
		coloredWorkload("old", 2, 0, 2), coloredWorkload("new", 2, 2, 2),
		coloredPod("web-old-a", "old", false, "Terminating"),
		coloredPod("web-new-a", "new", true, ""), coloredPod("web-new-b", "new", true, ""),
	})
	require.Equal(t, module.HealthHealthy, evaluation.Health)
	require.NotContains(t, codesOf(evaluation), "member-not-ready")
}

// A new color that exhausted its progress deadline is degraded while the
// previous color keeps serving; only a serving color with no ready member
// makes the service unhealthy.
func TestEvaluateBlueGreenFailedColor(t *testing.T) {
	t.Parallel()
	svc := decode(t)
	exceeded := module.Condition{Type: "Progressing", Status: "False", Reason: "ProgressDeadlineExceeded", Message: "timed out"}

	evaluation := svc.Evaluate([]module.ObservedResource{
		freshSource(), rollout("new", "old"),
		coloredWorkload("old", 2, 2, 2), coloredWorkload("new", 2, 0, 2, exceeded),
		coloredPod("web-new-a", "new", false, "CrashLoopBackOff"),
	})
	require.Equal(t, module.HealthDegraded, evaluation.Health)
	require.Equal(t, "color-failed", evaluation.Diagnostics[0].Code)
	require.Contains(t, codesOf(evaluation), "progress-deadline-exceeded")

	evaluation = svc.Evaluate([]module.ObservedResource{
		freshSource(), rollout("new", "old"),
		coloredWorkload("old", 2, 0, 2), coloredWorkload("new", 2, 1, 2),
	})
	require.Equal(t, module.HealthUnhealthy, evaluation.Health)
	require.Equal(t, "no-ready-replicas", evaluation.Diagnostics[0].Code)

	// Legacy migration: the serving color is the uncolored Deployment.
	evaluation = svc.Evaluate([]module.ObservedResource{
		freshSource(), rollout("new", ""),
		workload(2, 2, 2), coloredWorkload("new", 2, 2, 2),
	})
	require.Equal(t, module.HealthProgressing, evaluation.Health)
	require.Equal(t, "switching-traffic", evaluation.Diagnostics[0].Code)

	// No workload of any color: unknown, as before.
	evaluation = svc.Evaluate([]module.ObservedResource{freshSource(), rollout("new", "new")})
	require.Equal(t, module.HealthUnknown, evaluation.Health)
	require.Equal(t, "missing-resource", evaluation.Diagnostics[0].Code)
}
