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
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/utils"
)

// memberDiagnosticLimit bounds how many not-ready members one evaluation
// names; the count-based message already carries the totals.
const memberDiagnosticLimit = 5

type Module struct {
	// Certificates mirrors the installation's cert-manager capability: when
	// set, TLS routes gate health on their certificate's issuance, so a
	// deploy only activates once the route actually terminates TLS. Local
	// installations leave it false and route certificates are ignored.
	Certificates bool
}

func (Module) Type() string { return "application" }

func (m Module) Decode(definition compiler.ProjectDefinition, key string) (module.Service, error) {
	application, ok := definition.Applications[key]
	if !ok {
		return nil, fmt.Errorf("app: application %s is not defined", key)
	}
	dependencies := definition.Dependencies["applications."+key]
	return &service{key: key, project: definition.Name, application: application,
		dependencies: dependencies, certificates: m.Certificates}, nil
}

type service struct {
	key          string
	project      string
	application  compiler.Application
	dependencies []string
	certificates bool
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

// Evaluate projects application health from the observed snapshot: the
// workload verdict below, gated by route-certificate issuance on
// TLS-capable installations. A pending certificate keeps an otherwise
// healthy service progressing, so activation (and therefore the deploy
// run) waits for issuance and the rollout deadline turns a certificate
// that never issues into a failed run naming the reason.
func (s *service) Evaluate(observed []module.ObservedResource) module.Evaluation {
	evaluation := s.evaluateWorkload(observed)
	if !s.certificates || evaluation.Health == module.HealthUnknown {
		return evaluation
	}
	return s.applyCertificateGate(observed, evaluation, time.Now())
}

// evaluateWorkload draws the workload distinctions: progressing while a
// rollout is still moving (controller lag or members not yet updated),
// degraded or unhealthy by ready count once it is not, and unknown when
// observation cannot support a verdict (stale source, missing workload,
// zero desired members). Member and controller-condition diagnostics ride
// along so a degraded state always names its reason.
func (s *service) evaluateWorkload(observed []module.ObservedResource) module.Evaluation {
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

// applyCertificateGate folds route-certificate issuance into the workload
// verdict. Issuance in flight floors health to progressing, an expired
// certificate floors it to degraded (the route is genuinely down), and a
// failing renewal of a still-valid certificate stays a warning so it never
// blocks a later deploy. When the certificate is the only blocker its
// diagnostic leads, because the run journal and the failed verify step
// surface exactly the first diagnostic.
func (s *service) applyCertificateGate(observed []module.ObservedResource, evaluation module.Evaluation, now time.Time) module.Evaluation {
	health := evaluation.Health
	var blockers, notes []module.Diagnostic
	for _, routeKey := range utils.SortedKeys(s.application.Routes) {
		route := s.application.Routes[routeKey]
		if route.TLS == "disabled" {
			continue
		}
		name := kubernetes.RouteTLSName(s.project, s.key, routeKey)
		certificate := findCertificate(observed, name)
		switch {
		case certificate == nil:
			// The rendered Certificate has not reached the snapshot; the
			// -unobserved suffix asks the kernel to bounce the watch in case
			// it fell into an establishment gap.
			blockers = append(blockers, infoDiag("certificate-unobserved",
				"certificate "+name+" is not observed yet", name))
			health = floorHealth(health, module.HealthProgressing)
		case certificate.NotAfter.IsZero():
			// Never issued. Failed attempts make it an error so the deadline
			// failure names a cause, not a wait.
			message := "certificate " + name + " is not issued yet"
			if detail := certificateDetail(certificate); detail != "" {
				message += ": " + detail
			}
			if certificate.FailedAttempts > 0 {
				blockers = append(blockers, errorDiag("certificate-failing", message, name))
			} else {
				blockers = append(blockers, infoDiag("certificate-pending", message, name))
			}
			health = floorHealth(health, module.HealthProgressing)
		case now.After(certificate.NotAfter):
			blockers = append(blockers, errorDiag("certificate-expired",
				"certificate "+name+" expired "+certificate.NotAfter.UTC().Format(time.RFC3339), name))
			health = floorHealth(health, module.HealthDegraded)
		case !certificate.Ready:
			// Still valid, renewal failing: post-activation this must never
			// gate, only warn.
			message := "certificate " + name + " renewal is failing"
			if detail := certificateDetail(certificate); detail != "" {
				message += ": " + detail
			}
			notes = append(notes, warnDiag("certificate-renewal-failing", message, name))
		}
	}
	diagnostics := evaluation.Diagnostics
	if health != evaluation.Health {
		// The certificates are what blocks; their story leads.
		diagnostics = append(append([]module.Diagnostic{}, blockers...), diagnostics...)
	} else {
		diagnostics = append(diagnostics, blockers...)
	}
	diagnostics = append(diagnostics, notes...)
	return module.Evaluation{Health: health, Diagnostics: diagnostics}
}

func findCertificate(observed []module.ObservedResource, name string) *module.CertificateStatus {
	for _, resource := range observed {
		if resource.Kind == module.KindCertificate && resource.Name == name && resource.Certificate != nil {
			return resource.Certificate
		}
	}
	return nil
}

func certificateDetail(certificate *module.CertificateStatus) string {
	parts := []string{}
	if certificate.Reason != "" {
		parts = append(parts, certificate.Reason)
	}
	if certificate.Message != "" {
		parts = append(parts, certificate.Message)
	}
	return strings.Join(parts, ": ")
}

// floorHealth caps health at the given ceiling: a healthy service becomes
// progressing when issuance is in flight, while an already worse verdict
// keeps its own story.
func floorHealth(current, ceiling module.Health) module.Health {
	rank := map[module.Health]int{
		module.HealthHealthy:     3,
		module.HealthProgressing: 2,
		module.HealthDegraded:    1,
		module.HealthUnhealthy:   0,
	}
	if rank[current] < rank[ceiling] {
		return current
	}
	return ceiling
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
