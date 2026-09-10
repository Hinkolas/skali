package reconcile

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/journal"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/revision"
)

// defaultRetireDrain is how long a Deployment that stopped serving (the
// previous color after a switch, a superseded pending color, a legacy
// uncolored workload) keeps running before it is pruned. It covers endpoint
// propagation to kube-proxy and the edge; the application's own SIGTERM
// budget still applies at deletion.
const defaultRetireDrain = 15 * time.Second

// trafficPlan is the kernel's per-application blue-green decision for one
// pass: the color the revision renders (desired), the color the live
// Service selects (serving), and whether this pass switches traffic.
type trafficPlan struct {
	key        string
	desired    string // rendered color; "" for rolling and recreate workloads
	serving    string // live Service selector color; "" when uncolored
	hasService bool   // a live Service exists to hold or switch
	switching  bool   // the desired workload is available: this pass switches
	// pendingReplicas sizes a pending autoscaled color to the serving
	// count; nil leaves the replica field to the renderer.
	pendingReplicas *int32
	// desiredName and servingName are the Deployment names on both sides.
	desiredName string
	servingName string
	// members is the desired color's replica count for progress messages;
	// ready its ready members.
	members int32
	ready   int32
}

// held reports whether the Service must keep selecting the serving color
// this pass because the desired workload is not yet fully available.
func (p trafficPlan) held() bool {
	return p.hasService && p.serving != p.desired && !p.switching
}

// planTraffic decides, per non-intercepted application, whether the Service
// keeps its current color or switches to the rendered one. The serving color
// is read from the live Service selector, never from stored intent, so a
// switch rests on what the cluster routes today. The desired workload must
// be fully available before traffic moves; until then the Service is held.
// Applications leaving blue-green (desired "") take the same path: the
// colored Deployment serves until the uncolored one is available.
func planTraffic(rev *revision.Revision, colors map[string]string, snapshot observe.Snapshot,
	namespace string, intercepts map[string]map[string]int32) map[string]trafficPlan {
	plans := make(map[string]trafficPlan, len(rev.Definition.Applications))
	for key, application := range rev.Definition.Applications {
		if _, intercepted := intercepts[key]; intercepted {
			continue
		}
		plan := trafficPlan{key: key, desired: colors[key]}
		plan.desiredName = deploymentNameFor(rev.Project, key, plan.desired)
		service := liveObjectByRef(snapshot, module.KindService, namespace, rendering.ApplicationName(rev.Project, key))
		if service == nil || service.Selector == nil {
			// No Service yet (first deploy) or an intercepted one: nothing
			// holds traffic; the rendered Service selects the desired color.
			plan.serving = plan.desired
			plan.servingName = plan.desiredName
			plans[key] = plan
			continue
		}
		plan.hasService = true
		plan.serving = service.Color
		plan.servingName = deploymentNameFor(rev.Project, key, plan.serving)
		if plan.serving == plan.desired {
			plans[key] = plan
			continue
		}

		autoscaled := application.Scaling.MaxReplicas > application.Scaling.MinReplicas
		desired := liveObjectByRef(snapshot, module.KindWorkload, namespace, plan.desiredName)
		members := int32(application.Scaling.MinReplicas)
		if autoscaled {
			// The pending color runs at the serving count; the autoscaler
			// keeps steering the serving color until the switch.
			count := servingCount(snapshot, namespace, plan.servingName, key, application)
			plan.pendingReplicas = &count
			members = count
		}
		plan.members = members
		if desired != nil && desired.Workload != nil {
			plan.ready = desired.Workload.Ready
			plan.switching = module.WorkloadAvailable(desired.Workload, members)
		}
		plans[key] = plan
	}
	return plans
}

// deploymentNameFor names an application's Deployment for one color; the
// empty color is the uncolored (rolling, recreate, legacy) Deployment.
func deploymentNameFor(project, key, color string) string {
	if color == "" {
		return rendering.ApplicationName(project, key)
	}
	return rendering.ColoredApplicationName(project, key, color)
}

// servingCount is the replica count the serving color runs at, clamped to
// the application's bounds: the workload's own spec when skalid owns it,
// the autoscaler's desire when the autoscaler does, the minimum otherwise.
func servingCount(snapshot observe.Snapshot, namespace, servingName, key string, application compiler.Application) int32 {
	count := int32(-1)
	if serving := liveObjectByRef(snapshot, module.KindWorkload, namespace, servingName); serving != nil && serving.Workload != nil {
		count = serving.Workload.Desired
	}
	if count < 0 {
		if autoscaler := liveObject(snapshot, key, module.KindAutoscaler); autoscaler != nil && autoscaler.Autoscaler != nil {
			count = autoscaler.Autoscaler.DesiredReplicas
			if count <= 0 {
				count = autoscaler.Autoscaler.Current
			}
		}
	}
	return max(int32(application.Scaling.MinReplicas), min(count, int32(application.Scaling.MaxReplicas)))
}

// trafficOptions folds the plans into render options: held applications pin
// their Service to the serving color, pending autoscaled colors get their
// replica count.
func trafficOptions(plans map[string]trafficPlan) (colors map[string]string, pending map[string]int32) {
	for key, plan := range plans {
		if !plan.held() {
			continue
		}
		if colors == nil {
			colors = map[string]string{}
		}
		colors[key] = plan.serving
		if plan.pendingReplicas != nil {
			if pending == nil {
				pending = map[string]int32{}
			}
			pending[key] = *plan.pendingReplicas
		}
	}
	return colors, pending
}

// retireKey identifies one Deployment in the retirement timers.
type retireKey struct {
	namespace string
	name      string
}

// retirements protects, per pass, the Deployments that must outlive the
// desired set: the serving color while a switch is pending, and every
// Deployment that stopped serving (previous color, superseded pending color,
// legacy uncolored workload) for the drain window after it was first seen
// not serving. Timers live on the kernel; after a restart they re-arm and
// a retired Deployment lives at most one extra window. The returned requeue
// is the time until the next retirement falls due, zero when none pends.
func (k *Kernel) retirements(plans map[string]trafficPlan, snapshot observe.Snapshot, now time.Time) (protected map[retireKey]bool, requeue time.Duration) {
	protected = map[retireKey]bool{}
	seen := map[retireKey]bool{}
	k.retireMu.Lock()
	defer k.retireMu.Unlock()
	for _, obj := range snapshot.Objects {
		if obj.Kind != module.KindWorkload {
			continue
		}
		plan, planned := plans[obj.Service]
		if !planned {
			continue
		}
		key := retireKey{obj.Ref.Namespace, obj.Ref.Name}
		seen[key] = true
		switch {
		case obj.Ref.Name == plan.desiredName:
			delete(k.retired, key)
		case plan.held() && obj.Ref.Name == plan.servingName:
			// Carries traffic until the switch: never a retirement.
			delete(k.retired, key)
			protected[key] = true
		default:
			armed, ok := k.retired[key]
			if !ok {
				armed = now
				k.retired[key] = armed
			}
			due := armed.Add(k.cfg.RetireDrain)
			if remaining := due.Sub(now); remaining > 0 {
				protected[key] = true
				if requeue == 0 || remaining < requeue {
					requeue = remaining
				}
			}
		}
	}
	for key := range k.retired {
		if key.namespace == snapshotNamespace(snapshot) && !seen[key] {
			delete(k.retired, key)
		}
	}
	return protected, requeue
}

// snapshotNamespace is the environment namespace of a snapshot's objects,
// empty for an empty snapshot; retirement timers are scoped by it.
func snapshotNamespace(snapshot observe.Snapshot) string {
	for _, obj := range snapshot.Objects {
		if obj.Ref.Namespace != "" && obj.Kind == module.KindWorkload {
			return obj.Ref.Namespace
		}
	}
	return ""
}

// rolloutBudget is the deadline a rollout gets before the target falls
// back: the longest per-application rollout timeout the revision declares,
// or the kernel default when none does.
func rolloutBudget(definition compiler.ProjectDefinition, fallback time.Duration) time.Duration {
	budget := fallback
	for _, application := range definition.Applications {
		if timeout := time.Duration(application.Deployment.Rollout.TimeoutMillis) * time.Millisecond; timeout > budget {
			budget = timeout
		}
	}
	return budget
}

// journalTraffic narrates the blue-green switch on the attached run: a
// waiting step while the new color starts, a completed step the moment
// traffic moves.
func journalTraffic(ctx context.Context, attachment *runAttachment, plans map[string]trafficPlan, drain time.Duration) {
	if attachment == nil || attachment.run == nil {
		return
	}
	for _, key := range sortedPlanKeys(plans) {
		plan := plans[key]
		switch {
		case plan.switching:
			attachment.completeStep(ctx, "rollout:"+key, "Roll out "+key, journal.StepSucceeded, []string{
				fmt.Sprintf("switching traffic to %d new replicas", plan.members),
				fmt.Sprintf("retiring previous replicas after %s", drain.Truncate(time.Second)),
			})
		case plan.held():
			attachment.waitStep(ctx, "rollout:"+key, "Roll out "+key,
				fmt.Sprintf("starting %d new replicas (%d/%d ready)", plan.members, plan.ready, plan.members))
		}
	}
}

func sortedPlanKeys(plans map[string]trafficPlan) []string {
	keys := make([]string, 0, len(plans))
	for key := range plans {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
