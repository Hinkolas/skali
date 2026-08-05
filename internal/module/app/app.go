// Package app is the production application module: the service-module
// contract implementation for manifest applications (build or image
// sourced) running as Deployment, Service, routing, and autoscaling. Its
// health evaluation is pure projection over the ObservedStore snapshot:
// rollout progress from workload generations and update counts, member
// diagnostics from pod states, and controller conditions, with the shared
// stale-source guard short-circuiting to unknown. This module is what the
// daemon registers and what the kernel and API tests exercise.
package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/module"
)

// memberDiagnosticLimit bounds how many not-ready members one evaluation
// names; the count-based message already carries the totals.
const memberDiagnosticLimit = 5

type Module struct{}

func (Module) Type() string { return "application" }

func (Module) Decode(definition compiler.ProjectDefinition, key string) (module.Service, error) {
	application, ok := definition.Applications[key]
	if !ok {
		return nil, fmt.Errorf("app: application %s is not defined", key)
	}
	dependencies := definition.Dependencies["applications."+key]
	return &service{key: key, application: application, dependencies: dependencies}, nil
}

type service struct {
	key          string
	application  compiler.Application
	dependencies []string
}

func (s *service) Key() string  { return s.key }
func (s *service) Type() string { return "application" }

func (s *service) Dependencies() []string {
	out := make([]string, len(s.dependencies))
	copy(out, s.dependencies)
	sort.Strings(out)
	return out
}

func (s *service) Outputs() []string { return nil }

func (s *service) Artifacts() []module.ArtifactRequirement {
	return []module.ArtifactRequirement{{Application: s.key, Source: s.application.Source}}
}

func (s *service) Steps() []module.Step {
	return []module.Step{
		{Key: "prepare-artifact:" + s.key, Title: "Prepare artifact for " + s.key},
		{Key: "apply:" + s.key, Title: "Apply " + s.key},
		{Key: "verify:" + s.key, Title: "Verify " + s.key},
	}
}

func (s *service) Removal() module.Removal {
	if len(s.application.Volumes) > 0 {
		return module.Removal{
			DataLoss:    true,
			Description: "removes the running application and deletes its persistent volumes",
		}
	}
	return module.Removal{
		DataLoss:    false,
		Description: "removes the running application; no stored data",
	}
}

// Evaluate projects application health from the observed snapshot. The
// distinctions it draws: progressing while a rollout is still moving
// (controller lag or members not yet updated), degraded or unhealthy by
// ready count once it is not, and unknown when observation cannot support
// a verdict (stale source, missing workload, zero desired members). Member
// and controller-condition diagnostics ride along so a degraded state
// always names its reason.
func (s *service) Evaluate(observed []module.ObservedResource) module.Evaluation {
	if source := module.StaleSource(observed); source != nil {
		message := "observation source state is " + source.State
		if !source.StaleSince.IsZero() {
			message = "observation is stale since " + source.StaleSince.UTC().Format(time.RFC3339)
		}
		return module.Evaluation{Health: module.HealthUnknown, Diagnostics: []module.Diagnostic{{
			Severity: "warning", Code: "observation_stale_since", Message: message,
		}}}
	}

	workload := findWorkload(observed, s.key)
	if workload == nil {
		return module.Evaluation{Health: module.HealthUnknown, Diagnostics: []module.Diagnostic{{
			Severity: "error", Code: "missing-resource",
			Message: "no workload observed for " + s.key,
		}}}
	}

	desired := workload.Desired
	if desired < 0 {
		// The autoscaler owns the replica count; adopt its desire when
		// observed, otherwise the count stays unknowable.
		desired = autoscalerDesire(observed, s.key)
	}
	if desired <= 0 {
		return module.Evaluation{Health: module.HealthUnknown, Diagnostics: []module.Diagnostic{{
			Severity: "warning", Code: "no-replicas",
			Message: "the workload requests zero replicas", Resource: s.key,
		}}}
	}

	// Controller lag: the spec generation has not been observed yet, so
	// every count below would describe the previous revision.
	if workload.ObservedGeneration > 0 && workload.ObservedGeneration < workload.Generation {
		return module.Evaluation{Health: module.HealthProgressing, Diagnostics: []module.Diagnostic{{
			Severity: "info", Code: "rollout-pending",
			Message: "the controller has not observed the latest revision yet", Resource: s.key,
		}}}
	}

	diagnostics := append(conditionDiagnostics(workload, s.key), memberDiagnostics(observed)...)
	deadlineExceeded := hasCondition(workload, "Progressing", "False", "ProgressDeadlineExceeded")
	ready := workload.Ready

	switch {
	case ready >= desired && workload.Updated >= desired && !deadlineExceeded:
		return module.Evaluation{Health: module.HealthHealthy, Diagnostics: diagnostics}
	case deadlineExceeded && ready == 0:
		return module.Evaluation{Health: module.HealthUnhealthy, Diagnostics: prepend(diagnostics,
			errorDiag("no-ready-replicas", fmt.Sprintf("0/%d members ready", desired), s.key))}
	case deadlineExceeded:
		return module.Evaluation{Health: module.HealthDegraded, Diagnostics: prepend(diagnostics,
			warnDiag("partial-availability", fmt.Sprintf("%d/%d members ready", ready, desired), s.key))}
	case workload.Updated < desired:
		return module.Evaluation{Health: module.HealthProgressing, Diagnostics: prepend(diagnostics,
			infoDiag("rolling-update", fmt.Sprintf("%d/%d members updated", workload.Updated, desired), s.key))}
	case ready == 0:
		return module.Evaluation{Health: module.HealthUnhealthy, Diagnostics: prepend(diagnostics,
			errorDiag("no-ready-replicas", fmt.Sprintf("0/%d members ready", desired), s.key))}
	default:
		return module.Evaluation{Health: module.HealthDegraded, Diagnostics: prepend(diagnostics,
			warnDiag("partial-availability", fmt.Sprintf("%d/%d members ready", ready, desired), s.key))}
	}
}

func findWorkload(observed []module.ObservedResource, key string) *module.WorkloadStatus {
	for _, resource := range observed {
		if resource.Kind == module.KindWorkload && resource.Name == key && resource.Workload != nil {
			return resource.Workload
		}
	}
	return nil
}

func autoscalerDesire(observed []module.ObservedResource, key string) int32 {
	for _, resource := range observed {
		if resource.Kind == module.KindAutoscaler && resource.Name == key && resource.Autoscaler != nil {
			return resource.Autoscaler.DesiredReplicas
		}
	}
	return 0
}

// memberDiagnostics names not-ready members with their reason and restart
// count: the "web-abc: CrashLoopBackOff, 3 restarts" line the CLI and web
// surface directly.
func memberDiagnostics(observed []module.ObservedResource) []module.Diagnostic {
	var diagnostics []module.Diagnostic
	for _, resource := range observed {
		if resource.Kind != module.KindPod || resource.Pod == nil {
			continue
		}
		pod := resource.Pod
		if pod.Ready || pod.Phase == "Succeeded" {
			continue
		}
		if len(diagnostics) == memberDiagnosticLimit {
			diagnostics = append(diagnostics, module.Diagnostic{
				Severity: "warning", Code: "member-not-ready",
				Message: "further members are not ready",
			})
			break
		}
		parts := []string{}
		if pod.Reason != "" {
			parts = append(parts, pod.Reason)
		} else if pod.Phase != "" {
			parts = append(parts, pod.Phase)
		}
		if pod.Restarts > 0 {
			parts = append(parts, fmt.Sprintf("%d restarts", pod.Restarts))
		}
		if pod.Message != "" {
			parts = append(parts, pod.Message)
		}
		message := resource.Name
		if len(parts) > 0 {
			message += ": " + strings.Join(parts, ", ")
		}
		severity := "warning"
		if pod.Reason == "CrashLoopBackOff" || pod.Reason == "ImagePullBackOff" ||
			pod.Reason == "ErrImagePull" || pod.Phase == "Failed" {
			severity = "error"
		}
		diagnostics = append(diagnostics, module.Diagnostic{
			Severity: severity, Code: "member-not-ready",
			Message: message, Resource: resource.Name,
		})
	}
	return diagnostics
}

// conditionDiagnostics surfaces controller verdicts that counts alone hide:
// an exceeded progress deadline and replica creation failures.
func conditionDiagnostics(workload *module.WorkloadStatus, key string) []module.Diagnostic {
	var diagnostics []module.Diagnostic
	for _, condition := range workload.Conditions {
		switch {
		case condition.Type == "Progressing" && condition.Status == "False" &&
			condition.Reason == "ProgressDeadlineExceeded":
			diagnostics = append(diagnostics, errorDiag("progress-deadline-exceeded",
				"the rollout exceeded its progress deadline: "+condition.Message, key))
		case condition.Type == "ReplicaFailure" && condition.Status == "True":
			diagnostics = append(diagnostics, errorDiag("replica-failure",
				"members cannot be created: "+condition.Message, key))
		}
	}
	return diagnostics
}

func hasCondition(workload *module.WorkloadStatus, kind, status, reason string) bool {
	for _, condition := range workload.Conditions {
		if condition.Type == kind && condition.Status == status && condition.Reason == reason {
			return true
		}
	}
	return false
}

func prepend(diagnostics []module.Diagnostic, leading module.Diagnostic) []module.Diagnostic {
	return append([]module.Diagnostic{leading}, diagnostics...)
}

func infoDiag(code, message, resource string) module.Diagnostic {
	return module.Diagnostic{Severity: "info", Code: code, Message: message, Resource: resource}
}

func warnDiag(code, message, resource string) module.Diagnostic {
	return module.Diagnostic{Severity: "warning", Code: code, Message: message, Resource: resource}
}

func errorDiag(code, message, resource string) module.Diagnostic {
	return module.Diagnostic{Severity: "error", Code: code, Message: message, Resource: resource}
}
