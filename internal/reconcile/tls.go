package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
)

// reconcileTLS gives each desired route its own checkpoint. Recovery is
// driven by promotion time and live cluster state, never journal history.
func (k *Kernel) reconcileTLS(ctx context.Context, a *runAttachment, target store.EnvironmentTarget, rev *revision.Revision, desired *desiredSet, snapshot observe.Snapshot) (blocked, failed bool) {
	if !k.cfg.Certificates {
		return false, false
	}
	rollout := target.ActiveRevisionID == nil || *target.ActiveRevisionID != *target.TargetRevisionID
	attached := a.adopted() && rolloutRun(a.run.Kind)
	if !rollout && !attached {
		return false, false
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
		valid := cert != nil && cert.NotAfter.After(now)
		readOK := true
		if cert != nil && !valid {
			if rollout && k.deps.RetryCertificate != nil && !cert.Issuing && !cert.LastFailureTime.IsZero() && cert.LastFailureTime.Before(target.UpdatedAt.Truncate(time.Second)) && now.Before(deadline) {
				retryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				changed, err := k.deps.RetryCertificate(retryCtx, ref, target.UpdatedAt)
				cancel()
				if err != nil {
					fields["recovery_error"] = err.Error()
					readOK = false
				} else if changed {
					slog.InfoContext(ctx, "TLS issuance retry triggered", "environment_id", target.EnvironmentID, "certificate", ref.Name, "failed_attempts", cert.FailedAttempts)
					// The successful status write is fresher than the informer. Keep
					// the wait open until the controller's next state arrives.
					copy := *cert
					copy.Issuing = true
					copy.Ready = false
					copy.NextRetryTime = time.Time{}
					copy.Reason = "ManuallyTriggered"
					copy.Message = "Redeploy requested a fresh issuance attempt"
					cert = &copy
				}
			}
			if attached && k.deps.InspectCertificate != nil {
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
		}
		if cert != nil {
			valid = cert.NotAfter.After(now)
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
			case valid:
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
				detail = fmt.Sprintf("Issuing TLS certificate · attempt %d", cert.FailedAttempts+1)
			case !cert.LastFailureTime.IsZero():
				fields["phase"] = "backoff"
				fields["issuance_attempt"] = max(cert.FailedAttempts, 1)
				fields["next_attempt"] = cert.FailedAttempts + 1
				detail = fmt.Sprintf("TLS issuance failed · attempt %d", max(cert.FailedAttempts, 1))
				level = "warn"
				if !cert.NextRetryTime.IsZero() {
					fields["next_retry_at"] = cert.NextRetryTime.UTC().Format(time.RFC3339)
					fields["retry_estimated"] = true
				}
			default:
				fields["issuance_attempt"] = cert.FailedAttempts + 1
				detail = "Waiting for TLS issuance"
			}
		}
		state := journal.StepWaiting
		if valid {
			state = journal.StepSucceeded
		} else {
			blocked = true
			// Only a failure of this promotion can fail early. A pre-existing
			// failure gets its recovery opportunity, including controller/API lag.
			hopeless := readOK && cert != nil && !cert.Issuing && !cert.LastFailureTime.Before(target.UpdatedAt.Truncate(time.Second)) && cert.NextRetryTime.After(deadline)
			if now.After(deadline) || hopeless {
				failed = true
				state = journal.StepFailed
				level = "error"
				if hopeless {
					fields["failure"] = "Next automatic retry is after the rollout deadline"
				} else {
					fields["failure"] = "TLS issuance exceeded the rollout deadline"
				}
				fields["guidance"] = "Check public A/AAAA DNS and HTTP port 80 reachability, then redeploy. If the CA reports a rate limit, wait for its retry window before redeploying."
			}
		}
		if attached {
			a.tlsStep(ctx, "tls:"+ref.Name, title, state, level, detail, fields)
		}
	}
	return blocked, failed
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
	if state != journal.StepWaiting {
		if err := a.journal.SetStepStatus(ctx, step.ID, journal.StepRunning); err != nil {
			warn("start TLS checkpoint", err)
			return
		}
		if err := a.journal.SetStepStatus(ctx, step.ID, state); err != nil {
			warn("finish TLS checkpoint", err)
		}
	}
}
