package module

import (
	"fmt"
	"strings"
	"time"
)

// CertRoute is one TLS-capable route a service owns, named by its
// Certificate object (the renderer's RouteTLSName or BucketRouteTLSName).
type CertRoute struct {
	Name string
	TLS  string
}

// CertificateGate folds route-certificate issuance into a service's
// evaluation: issuance in flight floors health to progressing with the
// certificate's story leading, an expired certificate degrades, and a
// route whose domain does not reach this installation yet (the edge
// verdict says deferred) only warns, so a DNS move never parks a rollout.
// After activation a renewal failure is a warning, never a gate. It is
// shared by every module that renders routes; the routes are given in
// manifest order so diagnostics are stable.
func CertificateGate(observed []ObservedResource, routes []CertRoute, evaluation Evaluation, now time.Time) Evaluation {
	health := evaluation.Health
	var blockers, notes []Diagnostic
	for _, route := range routes {
		if route.TLS == "disabled" {
			continue
		}
		name := route.Name
		certificate := findCertificate(observed, name)
		edge := findEdge(observed, name)
		valid := certificate != nil && !certificate.NotAfter.IsZero() && now.Before(certificate.NotAfter)
		// A valid certificate issued for other names (the route's domain
		// changed on the same key) is unissued for the desired domain.
		mismatch := valid && edge != nil && edge.Mismatch
		covered := valid && !mismatch
		if edge != nil && edge.Deferred && !covered {
			message := "certificate " + name + " is deferred: " + edge.Domain + " does not reach this installation yet (" + edge.State + ")"
			if mismatch {
				message += "; the certificate for " + strings.Join(edge.Issued, ", ") + " keeps serving"
			}
			notes = append(notes, gateDiag("warning", "certificate-deferred", message, name))
			continue
		}
		switch {
		case certificate == nil:
			// The rendered Certificate has not reached the snapshot; the
			// -unobserved suffix asks the kernel to bounce the watch in case
			// it fell into an establishment gap.
			blockers = append(blockers, gateDiag("info", "certificate-unobserved",
				"certificate "+name+" is not observed yet", name))
			health = floorHealth(health, HealthProgressing)
		case certificate.NotAfter.IsZero():
			// Never issued. Failed attempts make it an error so the deadline
			// failure names a cause, not a wait.
			message := "certificate " + name + " is not issued yet"
			if detail := certificateDetail(certificate); detail != "" {
				message += ": " + detail
			}
			if certificate.FailedAttempts > 0 {
				blockers = append(blockers, gateDiag("error", "certificate-failing", message, name))
			} else {
				blockers = append(blockers, gateDiag("info", "certificate-pending", message, name))
			}
			health = floorHealth(health, HealthProgressing)
		case now.After(certificate.NotAfter):
			blockers = append(blockers, gateDiag("error", "certificate-expired",
				"certificate "+name+" expired "+certificate.NotAfter.UTC().Format(time.RFC3339), name))
			health = floorHealth(health, HealthDegraded)
		case mismatch:
			// The domain reaches this installation but the certificate on
			// hand names the old domain: issuance for the new one gates
			// like a first issuance, and its failures name a cause.
			message := "certificate " + name + " is not issued for " + edge.Domain + " yet"
			if len(edge.Issued) > 0 {
				message += " (issued for " + strings.Join(edge.Issued, ", ") + ")"
			}
			if detail := certificateDetail(certificate); detail != "" {
				message += ": " + detail
			}
			if certificate.FailedAttempts > 0 {
				blockers = append(blockers, gateDiag("error", "certificate-failing", message, name))
			} else {
				blockers = append(blockers, gateDiag("info", "certificate-pending", message, name))
			}
			health = floorHealth(health, HealthProgressing)
		case !certificate.Ready:
			// Still valid, renewal failing: post-activation this must never
			// gate, only warn.
			message := "certificate " + name + " renewal is failing"
			if detail := certificateDetail(certificate); detail != "" {
				message += ": " + detail
			}
			notes = append(notes, gateDiag("warning", "certificate-renewal-failing", message, name))
		}
	}
	diagnostics := evaluation.Diagnostics
	if health != evaluation.Health {
		// The certificates are what blocks; their story leads.
		diagnostics = append(append([]Diagnostic{}, blockers...), diagnostics...)
	} else {
		diagnostics = append(diagnostics, blockers...)
	}
	diagnostics = append(diagnostics, notes...)
	return Evaluation{Health: health, Diagnostics: diagnostics}
}

func gateDiag(severity, code, message, resource string) Diagnostic {
	return Diagnostic{Severity: severity, Code: code, Message: message, Resource: resource}
}

func findCertificate(observed []ObservedResource, name string) *CertificateStatus {
	for _, resource := range observed {
		if resource.Kind == KindCertificate && resource.Name == name && resource.Certificate != nil {
			return resource.Certificate
		}
	}
	return nil
}

func findEdge(observed []ObservedResource, name string) *EdgeReach {
	for _, resource := range observed {
		if resource.Kind == KindEdge && resource.Name == name && resource.Edge != nil {
			return resource.Edge
		}
	}
	return nil
}

func certificateDetail(certificate *CertificateStatus) string {
	parts := []string{}
	if certificate.FailedAttempts > 0 {
		parts = append(parts, fmt.Sprintf("%d failed issuance attempts", certificate.FailedAttempts))
	}
	if certificate.Issuing {
		parts = append(parts, fmt.Sprintf("issuing attempt %d", certificate.FailedAttempts+1))
	}
	if !certificate.NextRetryTime.IsZero() {
		parts = append(parts, "next retry estimated at "+certificate.NextRetryTime.UTC().Format(time.RFC3339))
	}
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
func floorHealth(current, ceiling Health) Health {
	rank := map[Health]int{
		HealthHealthy:     3,
		HealthProgressing: 2,
		HealthDegraded:    1,
		HealthUnhealthy:   0,
	}
	if rank[current] < rank[ceiling] {
		return current
	}
	return ceiling
}
