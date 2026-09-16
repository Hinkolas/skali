package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/edge/edgeobserve"
	"github.com/Hinkolas/skali/internal/edge/edgeprobe"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
)

// tlsOutcome is what one TLS pass tells the reconciler: whether an
// unissued certificate withholds activation, whether the rollout (or a
// converged environment's issuance after its domain arrived) has failed on
// it, and how soon the pass wants to run again while a route domain is
// still on its way to this edge.
type tlsOutcome struct {
	blocked bool
	failed  bool
	requeue time.Duration
}

// deferredGuidance is the operator's next step on a deferred route.
const deferredGuidance = "Point the domain's A/AAAA records at this installation; the certificate is issued automatically once requests arrive here."

// issuanceGuidance is the operator's next step on a failed issuance.
const issuanceGuidance = "Check public A/AAAA DNS and HTTP port 80 reachability, then redeploy. If the CA reports a rate limit, wait for its retry window before redeploying."

// arrivalNote is what a converged pass records when a route's domain
// reached this edge: the step outcome and its closing line.
type arrivalNote struct {
	status journal.StepStatus
	line   string
}

// reconcileTLS gives each desired route its own checkpoint. Recovery is
// driven by promotion time and live cluster state, never journal history.
//
// A certificate is usable when it is valid and was issued for the domain
// the route wants; a valid certificate issued for other names (the route's
// domain changed on the same key, the migration case) is unissued for the
// desired domain, while the edge keeps serving it for the old one.
//
// A route whose domain does not reach this installation's edge (probed
// through Deps.ProbeDomain) is deferred: it neither blocks activation nor
// fails the rollout, its checkpoint ends skipped with a warning naming the
// domain, and the environment keeps re-probing on a slow cadence. When the
// domain arrives, a certificate parked in cert-manager's failure backoff
// gets one fresh issuance attempt right away; a converged environment
// records the arrival as a run and, in a later pass, the outcome.
//
// The probes themselves ran before the pass took the environment lock
// (prepareEdgeVerdicts, on the cadence interval); under the lock this only
// reads the cache. A domain without a verdict counts as unknown and the
// pass requeues quickly so the next pre-lock phase probes it.
func (k *Kernel) reconcileTLS(ctx context.Context, a *runAttachment, target store.EnvironmentTarget, rev *revision.Revision, desired *desiredSet, snapshot observe.Snapshot, interval time.Duration) tlsOutcome {
	var out tlsOutcome
	if !k.cfg.Certificates {
		return out
	}
	rollout := target.ActiveRevisionID == nil || *target.ActiveRevisionID != *target.TargetRevisionID
	attached := a.adopted() && rolloutRun(a.run.Kind)
	gate := rollout || attached
	if !gate && k.deps.ProbeDomain == nil {
		return out
	}
	now := time.Now()
	deadline := target.UpdatedAt.Add(rolloutBudget(rev.Definition, k.cfg.RolloutDeadline) + releaseBudget(rev.Definition))
	refs := make([]kube.ObjectRef, 0)
	for _, ref := range desired.refs {
		if ref.GVK == edge.CertificateGVK {
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	for _, ref := range refs {
		key := routeKeyOf(target.EnvironmentID, ref.Name)
		var cert *module.CertificateStatus
		var service, route string
		for appKey, app := range rev.Definition.Applications {
			for routeKey := range app.Routes {
				if rendering.RouteTLSName(rev.Definition.Name, appKey, routeKey) == ref.Name {
					service = appKey
					route = routeKey
				}
			}
		}
		for _, obj := range snapshot.Objects {
			if obj.Ref.GVK == ref.GVK && obj.Ref.Namespace == ref.Namespace && obj.Ref.Name == ref.Name {
				cert = obj.Certificate
				ref.UID = obj.Ref.UID
				break
			}
		}
		title := "Issue TLS certificate"
		if service != "" {
			title += " for " + service
		}
		if route != "" {
			title += " / " + route
		}
		fields := map[string]any{"tls": true, "certificate": ref.Name, "namespace": ref.Namespace, "phase": "pending", "deadline": deadline.UTC().Format(time.RFC3339)}
		detail := "Waiting for certificate observation"
		level := "info"
		readOK := true

		// The domain the route wants. The rendered Certificate names it
		// before the object is ever observed; the observed dnsNames are
		// the fallback.
		domain := desired.certDomains[ref.Name]
		if domain == "" && cert != nil && len(cert.DNSNames) > 0 {
			domain = cert.DNSNames[0]
		}
		// judge reads the certificate on hand: valid, and issued for the
		// domain the route wants. It runs again after a live refresh.
		var issued []string
		var valid, covered bool
		judge := func() {
			valid = cert != nil && cert.NotAfter.After(now)
			covered, issued = valid, nil
			if valid && domain != "" {
				issued = k.issuedNames(ctx, key, ref.Namespace, cert.SecretName, cert.NotAfter)
				covered = issued == nil || edgeobserve.Covers(issued, domain)
			}
		}
		judge()

		// The edge verdict for a route without a usable certificate.
		var probe *edgeprobe.Result
		arrived := false
		if !(valid && covered) && k.deps.ProbeDomain != nil && domain != "" {
			result, seen, known, fresh := k.lookupDomain(domain, now, interval)
			if !known {
				// No verdict yet: the route appeared after the pre-lock
				// probe phase, or a usable certificate just stopped
				// covering it. Unknown keeps the gate's default behaviour;
				// the next pass probes before it locks.
				result = edgeprobe.Result{Domain: domain, State: edgeprobe.StateUnknown,
					Message: "edge verdict pending", CheckedAt: now}
				seen = false
			}
			if !known || !fresh {
				out.requeue = soonest(out.requeue, edgeProbeMissRequeue)
			}
			probe, arrived = &result, seen
			if result.State.Pending() || result.State == edgeprobe.StateUnknown {
				out.requeue = soonest(out.requeue, interval)
			}
		}
		pending := probe != nil && probe.State.Pending()

		var arrival *arrivalNote
		if cert != nil && !(valid && covered) {
			if !pending && rollout && k.deps.RetryCertificate != nil && !cert.Issuing && !cert.LastFailureTime.IsZero() && cert.LastFailureTime.Before(target.UpdatedAt.Truncate(time.Second)) && now.Before(deadline) {
				retryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				changed, err := k.deps.RetryCertificate(retryCtx, ref, target.UpdatedAt)
				cancel()
				if err != nil {
					fields["recovery_error"] = err.Error()
					readOK = false
				} else if changed {
					slog.InfoContext(ctx, "TLS issuance retry triggered", "environment_id", target.EnvironmentID, "certificate", ref.Name, "failed_attempts", cert.FailedAttempts)
					cert = retriedCertificate(cert, "Redeploy requested a fresh issuance attempt")
				}
			}
			if probe != nil {
				switch {
				case cert.Issuing:
					// Issuance is already under way; the arrival needs no push.
					if arrived {
						arrival = &arrivalNote{journal.StepSucceeded, "Issuance is already under way; no fresh attempt is needed"}
					}
					k.settleArrival(probe.Domain)
				case arrived && k.deps.RetryCertificate != nil && !cert.LastFailureTime.IsZero():
					// The domain reached this edge for the first time since the
					// certificate last failed. Its failure post-dates the
					// promotion (the domain was elsewhere), so the retry is
					// measured against now, not the promotion time.
					retryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					changed, err := k.deps.RetryCertificate(retryCtx, ref, now)
					cancel()
					if err != nil {
						fields["recovery_error"] = err.Error()
						readOK = false
					} else if changed {
						slog.InfoContext(ctx, "TLS issuance retried: the route domain now reaches this edge", "environment_id", target.EnvironmentID, "certificate", ref.Name, "domain", probe.Domain)
						k.settleArrival(probe.Domain)
						cert = retriedCertificate(cert, "The domain now reaches this installation; a fresh issuance was requested")
						arrival = &arrivalNote{journal.StepSucceeded, fmt.Sprintf("A fresh issuance was requested · attempt %d", cert.FailedAttempts+1)}
					}
				}
			}
			// A domain that is elsewhere cannot validate; its controller
			// chain says nothing the verdict does not already say.
			if attached && !pending && k.deps.InspectCertificate != nil {
				inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				fresh, details, err := k.deps.InspectCertificate(inspectCtx, ref)
				cancel()
				if fresh != nil {
					cert = fresh
				}
				for key, value := range details {
					if value != "" {
						fields[key] = value
					}
				}
				if err != nil {
					fields["observation_error"] = err.Error()
					readOK = false
				}
			}
			judge()
		}
		usable := valid && covered
		record := k.noteRoute(key, domain, usable, pending && !usable, valid && !covered)
		serving := ""
		if valid && !covered {
			fields["issued_for"] = strings.Join(issued, ", ")
			serving = " · the certificate for " + strings.Join(issued, ", ") + " keeps serving"
		}

		if !gate {
			// Converged environment: the probe and the arrival retry are
			// the whole job; nothing gates. An arrival, and later the
			// outcome of the issuance it started, are the only events worth
			// a run of their own.
			if arrival != nil {
				k.recordArrival(ctx, a, key, ref.Name, domain, probe, cert, arrival, now)
			} else if !record.awaiting.IsZero() && cert != nil {
				if k.recordOutcome(ctx, a, key, ref.Name, title, domain, cert, usable, record.awaiting, now) {
					out.failed = true
				}
			}
			continue
		}
		if cert != nil {
			fields["domain"] = strings.Join(cert.DNSNames, ", ")
			fields["secret"] = cert.SecretName
			fields["failed_attempts"] = cert.FailedAttempts
			fields["reason"] = cert.Reason
			fields["message"] = cert.Message
			if cert.NextPrivateKeySecretName != "" {
				fields["next_private_key_secret"] = cert.NextPrivateKeySecretName
			}
			if !cert.LastFailureTime.IsZero() {
				fields["last_failure_at"] = cert.LastFailureTime.UTC().Format(time.RFC3339)
			}
			switch {
			case usable:
				fields["phase"] = "active"
				fields["valid_until"] = cert.NotAfter.UTC().Format(time.RFC3339)
				detail = "TLS certificate is valid"
				if !cert.Ready {
					detail = "TLS certificate is still valid; renewal needs attention"
					level = "warn"
				}
			case cert.Issuing:
				fields["phase"] = "issuing"
				fields["issuance_attempt"] = cert.FailedAttempts + 1
				detail = fmt.Sprintf("Issuing TLS certificate · attempt %d", cert.FailedAttempts+1) + serving
			case !cert.LastFailureTime.IsZero():
				fields["phase"] = "backoff"
				fields["issuance_attempt"] = max(cert.FailedAttempts, 1)
				fields["next_attempt"] = cert.FailedAttempts + 1
				detail = fmt.Sprintf("TLS issuance failed · attempt %d", max(cert.FailedAttempts, 1)) + serving
				level = "warn"
				if !cert.NextRetryTime.IsZero() {
					fields["next_retry_at"] = cert.NextRetryTime.UTC().Format(time.RFC3339)
					fields["retry_estimated"] = true
				}
			default:
				fields["issuance_attempt"] = cert.FailedAttempts + 1
				detail = "Waiting for TLS issuance" + serving
			}
		}
		if pending && !usable {
			// Deferred: the domain does not reach this edge, so the
			// certificate cannot validate and must not hold the rollout.
			// The checkpoint ends skipped with the verdict; no deadline and
			// no failure, because nothing here is failing.
			fields["phase"] = "deferred"
			fields["domain"] = probe.Domain
			fields["edge_state"] = string(probe.State)
			fields["edge_message"] = probe.Message
			fields["edge_checked_at"] = probe.CheckedAt.UTC().Format(time.RFC3339)
			fields["edge_addresses"] = edgeAddressField(probe.Addresses)
			fields["guidance"] = deferredGuidance
			delete(fields, "deadline")
			delete(fields, "issuance_attempt")
			delete(fields, "next_attempt")
			delete(fields, "next_retry_at")
			delete(fields, "retry_estimated")
			detail = fmt.Sprintf("TLS deferred · %s does not reach this installation yet", probe.Domain) + serving
			if attached {
				a.tlsStep(ctx, "tls:"+ref.Name, title, journal.StepSkipped, "warn", detail, fields)
			}
			continue
		}
		state := journal.StepWaiting
		if usable {
			state = journal.StepSucceeded
		} else {
			out.blocked = true
			// Only a failure of this promotion can fail early. A pre-existing
			// failure gets its recovery opportunity, including controller/API lag.
			hopeless := readOK && cert != nil && !cert.Issuing && !cert.LastFailureTime.Before(target.UpdatedAt.Truncate(time.Second)) && cert.NextRetryTime.After(deadline)
			if now.After(deadline) || hopeless {
				out.failed = true
				state = journal.StepFailed
				level = "error"
				if hopeless {
					fields["failure"] = "Next automatic retry is after the rollout deadline"
				} else {
					fields["failure"] = "TLS issuance exceeded the rollout deadline"
				}
				fields["guidance"] = issuanceGuidance
			}
		}
		if attached {
			a.tlsStep(ctx, "tls:"+ref.Name, title, state, level, detail, fields)
		}
	}
	return out
}

// recordArrival journals, on a converged environment, that a route's
// domain reached this edge and what the pass did about the certificate.
// The run it creates closes with the pass; the issuance outcome follows in
// a later pass through recordOutcome.
func (k *Kernel) recordArrival(ctx context.Context, a *runAttachment, key routeKey, certName, domain string,
	probe *edgeprobe.Result, cert *module.CertificateStatus, arrival *arrivalNote, now time.Time) {
	a.ensure(ctx)
	fields := map[string]any{
		"arrival":         true,
		"certificate":     certName,
		"domain":          domain,
		"edge_state":      string(probe.State),
		"edge_message":    probe.Message,
		"edge_checked_at": probe.CheckedAt.UTC().Format(time.RFC3339),
		"edge_addresses":  edgeAddressField(probe.Addresses),
	}
	if cert != nil {
		fields["issuance_attempt"] = cert.FailedAttempts + 1
	}
	lines := []string{domain + " now reaches this installation: " + probe.Message}
	lines = append(lines, edgeAddressLines(probe.Addresses)...)
	lines = append(lines, arrival.line)
	a.completeStepFields(ctx, "arrival:"+certName, "Domain arrived: "+domain, arrival.status, lines, fields)
	k.awaitIssuance(key, now)
}

// recordOutcome closes the story an arrival opened: the certificate became
// usable, or the issuance attempt after the arrival failed. Either clears
// the wait; a failure also fails the run this pass created.
func (k *Kernel) recordOutcome(ctx context.Context, a *runAttachment, key routeKey, certName, title, domain string,
	cert *module.CertificateStatus, usable bool, awaiting, now time.Time) (failed bool) {
	fields := map[string]any{"tls": true, "certificate": certName, "domain": domain, "secret": cert.SecretName,
		"failed_attempts": cert.FailedAttempts, "reason": cert.Reason, "message": cert.Message}
	switch {
	case usable:
		a.ensure(ctx)
		fields["phase"] = "active"
		fields["valid_until"] = cert.NotAfter.UTC().Format(time.RFC3339)
		a.completeStepFields(ctx, "tls:"+certName, title, journal.StepSucceeded,
			[]string{"TLS certificate issued for " + domain}, fields)
	case !cert.Issuing && !cert.LastFailureTime.Before(awaiting.Truncate(time.Second)):
		a.ensure(ctx)
		fields["phase"] = "backoff"
		fields["issuance_attempt"] = max(cert.FailedAttempts, 1)
		fields["last_failure_at"] = cert.LastFailureTime.UTC().Format(time.RFC3339)
		if !cert.NextRetryTime.IsZero() {
			fields["next_retry_at"] = cert.NextRetryTime.UTC().Format(time.RFC3339)
			fields["retry_estimated"] = true
		}
		fields["failure"] = "TLS issuance failed after the domain arrived"
		fields["guidance"] = issuanceGuidance
		line := fmt.Sprintf("TLS issuance failed after %s arrived · attempt %d", domain, max(cert.FailedAttempts, 1))
		if cert.Message != "" {
			line += ": " + cert.Message
		}
		a.completeStepFields(ctx, "tls:"+certName, title, journal.StepFailed, []string{line}, fields)
		failed = true
	default:
		return false
	}
	k.awaitIssuance(key, time.Time{})
	return failed
}

// retriedCertificate is the local view of a certificate whose issuance the
// kernel just re-triggered: the status write is fresher than the informer,
// so the wait stays open until the controller's next state arrives.
func retriedCertificate(cert *module.CertificateStatus, message string) *module.CertificateStatus {
	copy := *cert
	copy.Issuing = true
	copy.Ready = false
	copy.NextRetryTime = time.Time{}
	copy.Reason = "ManuallyTriggered"
	copy.Message = message
	return &copy
}

// tlsStep persists a structured snapshot when state changes. The readable
// message carries the same facts for CLI logs; stable absolute timestamps
// avoid writing a new row every second of a countdown.
func (a *runAttachment) tlsStep(ctx context.Context, key, title string, state journal.StepStatus, level, summary string, fields map[string]any) {
	step, err := a.journal.EnsureStep(ctx, a.run.ID, a.parent, key, title)
	if err != nil {
		warn("ensure TLS checkpoint", err)
		return
	}
	current := journal.StepStatus(step.Status)
	if journal.Steps.Terminal(current) {
		return
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if key != "tls" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := []string{summary}
	for _, key := range keys {
		if fields[key] != "" {
			lines = append(lines, strings.ReplaceAll(key, "_", " ")+": "+fmt.Sprint(fields[key]))
		}
	}
	message := a.redactor.Redact(strings.Join(lines, "\n"))
	// Match the journal bound before deduplicating long controller errors.
	const suffix = "... [truncated]"
	if len(message) > journal.MaxEntryBytes {
		message = message[:journal.MaxEntryBytes-len(suffix)] + suffix
	}
	if current == journal.StepPending {
		if err := a.journal.SetStepStatus(ctx, step.ID, journal.StepWaiting); err != nil {
			warn("wait TLS checkpoint", err)
			return
		}
	}
	last, err := a.journal.LatestStepMessage(ctx, step.ID)
	if err != nil {
		warn("read TLS checkpoint", err)
		return
	}
	if last != message {
		attempt, err := a.journal.StartAttempt(ctx, step.ID)
		if err != nil {
			warn("start TLS observation", err)
			return
		}
		if err := a.journal.Writer(attempt.ID, a.redactor).Log(ctx, level, message, fields); err != nil {
			warn("write TLS observation", err)
		}
		result := journal.AttemptSucceeded
		if state == journal.StepFailed {
			result = journal.AttemptFailed
		}
		if err := a.journal.FinishAttempt(ctx, attempt.ID, result); err != nil {
			warn("finish TLS observation", err)
		}
	}
	switch state {
	case journal.StepWaiting:
	case journal.StepSkipped:
		// A deferred route: the step machine allows waiting -> skipped
		// directly, and a skipped checkpoint reads as "set aside", which is
		// exactly what happened.
		if err := a.journal.SetStepStatus(ctx, step.ID, journal.StepSkipped); err != nil {
			warn("skip TLS checkpoint", err)
		}
	default:
		if err := a.journal.SetStepStatus(ctx, step.ID, journal.StepRunning); err != nil {
			warn("start TLS checkpoint", err)
			return
		}
		if err := a.journal.SetStepStatus(ctx, step.ID, state); err != nil {
			warn("finish TLS checkpoint", err)
		}
	}
}
