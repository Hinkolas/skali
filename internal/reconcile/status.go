package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/compiler"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/utils"
)

// Status is one environment's topology and health projection, assembled
// from the database pointers and the in-memory observed store: never from a
// request-time Kubernetes call.
type Status struct {
	EnvironmentID uuid.UUID
	State         string // environment_targets.state: active, down, or releasing
	Target        *RevisionRef
	Active        *RevisionRef
	Observation   module.SourceStatus
	Platforms     []string // observed application-node platforms, empty until observation syncs
	// PlatformPreference is the cluster's ordered build platform
	// preference from the kernel config; empty means multi-arch builds.
	PlatformPreference []string
	Services           []ServiceStatus
}

type RevisionRef struct {
	ID       uuid.UUID
	Checksum string
}

type ServiceStatus struct {
	Key         string
	Type        string
	Health      module.Health
	Diagnostics []module.Diagnostic
	Pods        []PodInfo
	// Routes projects the application's public routes with their edge
	// policies and, on TLS-capable installations, the observed certificate.
	Routes []RouteStatus
	// Intercepted marks an application served by a local dev process on
	// the host instead of a Deployment; its health is synthesized healthy
	// so activation and batch gating never wait on a workload that
	// deliberately does not exist.
	Intercepted bool
}

// RouteStatus is one public route of an application. Certificate is nil
// where no certificate exists by design: tls disabled, or an installation
// without cert-manager.
type RouteStatus struct {
	Key      string
	Domain   string
	Path     string
	TLS      string
	Strategy string

	Certificate *CertificateInfo
}

// CertificateInfo projects one route certificate's lifecycle for status
// surfaces. State is one of pending, issuing, active, failing, expired.
type CertificateInfo struct {
	Name        string
	SecretName  string
	State       string
	Reason      string
	Message     string
	NotAfter    time.Time
	RenewalTime time.Time
}

type PodInfo struct {
	Name     string
	Node     string
	Phase    string
	Ready    bool
	Restarts int32
	Reason   string
	Started  time.Time
	// Color is the pod's blue-green color, empty for uncolored workloads;
	// Serving reports whether the application's Service selects it.
	Color   string
	Serving bool
}

// Status projects one environment. The single database read resolves the
// pointers; everything else comes from the observed store.
func (k *Kernel) Status(ctx context.Context, environmentID uuid.UUID) (*Status, error) {
	target, err := k.deps.Store.GetEnvironmentTarget(ctx, environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("reconcile: get target: %w", err)
	}
	status := &Status{
		EnvironmentID:      environmentID,
		State:              target.State,
		Observation:        k.deps.Observed.Source(),
		Platforms:          k.deps.Observed.NodePlatforms(),
		PlatformPreference: k.cfg.PlatformPreference,
	}
	var targetRevision *revision.Revision
	if target.TargetRevisionID != nil {
		row, err := k.deps.Store.GetRevisionByID(ctx, *target.TargetRevisionID)
		if err != nil {
			return nil, fmt.Errorf("reconcile: get target revision: %w", err)
		}
		status.Target = &RevisionRef{ID: row.ID, Checksum: row.Checksum}
		targetRevision, err = revision.Decode(row.Document)
		if err != nil {
			return nil, err
		}
	}
	if target.ActiveRevisionID != nil {
		if status.Target != nil && *target.ActiveRevisionID == status.Target.ID {
			status.Active = status.Target
		} else {
			row, err := k.deps.Store.GetRevisionByID(ctx, *target.ActiveRevisionID)
			if err != nil {
				return nil, fmt.Errorf("reconcile: get active revision: %w", err)
			}
			status.Active = &RevisionRef{ID: row.ID, Checksum: row.Checksum}
		}
	}
	if targetRevision != nil {
		intercepts, err := k.loadIntercepts(ctx, environmentID)
		if err != nil {
			return nil, err
		}
		variables, err := k.routeVariables(ctx, environmentID, targetRevision)
		if err != nil {
			return nil, err
		}
		colors, err := k.desiredColors(ctx, environmentID, target, targetRevision, intercepts)
		if err != nil {
			return nil, err
		}
		status.Services = k.evaluateServices(targetRevision, k.deps.Observed.Snapshot(environmentID),
			intercepts, variables, colors)
	}
	return status, nil
}

// desiredColors names the blue-green color the target revision renders per
// application, from the same inputs the reconcile pass uses, so the status
// projection judges the same Deployment the kernel is switching to.
func (k *Kernel) desiredColors(ctx context.Context, environmentID uuid.UUID, target store.EnvironmentTarget,
	rev *revision.Revision, intercepts map[string]map[string]int32) (map[string]string, error) {
	env, err := k.deps.Store.GetEnvironmentByID(ctx, environmentID)
	if err != nil {
		return nil, fmt.Errorf("reconcile: get environment: %w", err)
	}
	appRestarts, err := k.loadAppRestarts(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	options, err := k.renderInputs(environmentID, rev, target.RestartedAt, appRestarts, intercepts, env.Priority)
	if err != nil {
		return nil, err
	}
	return rendering.ApplicationColors(&compiler.Result{Hash: rev.DefinitionHash, Definition: rev.Definition}, options)
}

// routeVariables loads the plaintexts of exactly the project variables the
// revision's route domains reference, so the status projection can show
// each route's resolved public hostname instead of its ${NAME} placeholder.
// A hostname is public by construction, so nothing beyond what the edge
// already serves leaves the store; every other value stays untouched.
func (k *Kernel) routeVariables(ctx context.Context, environmentID uuid.UUID,
	rev *revision.Revision) (map[string]string, error) {
	refs := map[string]int{}
	for _, application := range rev.Definition.Applications {
		for _, route := range application.Routes {
			for _, part := range route.Domain.Parts {
				if part.Kind != "project_variable" {
					continue
				}
				if secret, ok := rev.Secrets[part.Name]; ok {
					refs[part.Name] = secret.Version
				}
			}
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	variables, err := k.deps.Values.Plaintexts(ctx, environmentID, refs)
	if err != nil {
		return nil, fmt.Errorf("reconcile: resolve route variables: %w", err)
	}
	return variables, nil
}

// SubscribeStatus delivers invalidation nudges for one environment; the
// subscriber re-reads Status on every tick.
func (k *Kernel) SubscribeStatus(environmentID uuid.UUID) (<-chan observe.Invalidation, func()) {
	return k.deps.Observed.Subscribe(environmentID)
}

// evaluateServices projects per-service health through the registered
// modules, dispatching by service type; a missing module reports unknown
// with a diagnostic rather than guessing. Application objects index the
// observed store by bare key (the immutable label contract); database
// projections use the dotted form so keys can never collide across
// collections.
func (k *Kernel) evaluateServices(rev *revision.Revision, snapshot observe.Snapshot,
	intercepts map[string]map[string]int32, variables map[string]string, colors map[string]string) []ServiceStatus {
	type entry struct {
		key         string
		serviceType string
		observedKey string
		withPods    bool
	}
	entries := make([]entry, 0,
		len(rev.Definition.Applications)+len(rev.Definition.Databases)+len(rev.Definition.Buckets))
	for key := range rev.Definition.Applications {
		entries = append(entries, entry{key: key, serviceType: "application", observedKey: key, withPods: true})
	}
	for key := range rev.Definition.Databases {
		entries = append(entries, entry{key: key, serviceType: "database", observedKey: "databases." + key})
	}
	for key := range rev.Definition.Buckets {
		entries = append(entries, entry{key: key, serviceType: "bucket", observedKey: "buckets." + key})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].serviceType != entries[j].serviceType {
			return entries[i].serviceType < entries[j].serviceType
		}
		return entries[i].key < entries[j].key
	})

	statuses := make([]ServiceStatus, 0, len(entries))
	for _, item := range entries {
		status := ServiceStatus{Key: item.key, Type: item.serviceType}
		if item.withPods {
			status.Pods = podsFor(snapshot, item.key)
			status.Routes = routesFor(rev.Definition, snapshot, item.key, variables)
		}
		if _, ok := intercepts[item.key]; ok && item.serviceType == "application" {
			// Synthesized at the kernel, not in the app module: the module
			// stays free of store state, and its missing-workload verdict
			// would otherwise block activation and trip rollout fallback
			// for a Deployment that deliberately does not exist.
			status.Health = module.HealthHealthy
			status.Intercepted = true
			status.Diagnostics = []module.Diagnostic{{
				Severity: "info", Code: "intercepted",
				Message: "served by a local dev process on the host",
			}}
			statuses = append(statuses, status)
			continue
		}
		mod, registered := k.deps.Registry.Get(item.serviceType)
		if !registered {
			status.Health = module.HealthUnknown
			status.Diagnostics = []module.Diagnostic{{
				Severity: "warning", Code: "module-unavailable",
				Message: "no module registered for service type " + item.serviceType,
			}}
			statuses = append(statuses, status)
			continue
		}
		service, err := mod.Decode(rev.Definition, item.key)
		if err != nil {
			status.Health = module.HealthUnknown
			status.Diagnostics = []module.Diagnostic{{
				Severity: "error", Code: "decode-failed",
				Message: "decoding the service failed: " + err.Error(),
			}}
			statuses = append(statuses, status)
			continue
		}
		observed := snapshot.ForService(item.observedKey)
		if desiredColor, blueGreen := colors[item.key]; blueGreen && item.serviceType == "application" {
			// The kernel's intent rides along: the module judges the desired
			// color's workload and members against the color the live
			// Service selects, so a switch is only healthy once it landed.
			observed = append(observed, module.ObservedResource{
				Kind: module.KindRollout, Name: item.key,
				Rollout: &module.RolloutStatus{DesiredColor: desiredColor, ServingColor: servingColor(snapshot, item.key, desiredColor)},
			})
		}
		evaluation := service.Evaluate(observed)
		status.Health = evaluation.Health
		status.Diagnostics = evaluation.Diagnostics
		statuses = append(statuses, status)
	}
	return statuses
}

// routesFor joins the application's declared routes with the observed
// certificates, keyed by the rendered certificate name. The domain prefers
// the certificate's dnsNames (the value the edge actually serves), then
// the expression resolved against the revision's pinned variables; only
// when neither resolves does it fall back to the manifest expression.
func routesFor(definition compiler.ProjectDefinition, snapshot observe.Snapshot, key string,
	variables map[string]string) []RouteStatus {
	application, ok := definition.Applications[key]
	if !ok || len(application.Routes) == 0 {
		return nil
	}
	certificates := map[string]*module.CertificateStatus{}
	for index := range snapshot.Objects {
		obj := &snapshot.Objects[index]
		if obj.Kind == module.KindCertificate && obj.Service == key && obj.Certificate != nil {
			certificates[obj.Name] = obj.Certificate
		}
	}
	routes := make([]RouteStatus, 0, len(application.Routes))
	for _, routeKey := range utils.SortedKeys(application.Routes) {
		route := application.Routes[routeKey]
		status := RouteStatus{
			Key:      routeKey,
			Domain:   routeDomain(route.Domain, variables),
			Path:     route.Path,
			TLS:      route.TLS,
			Strategy: route.Strategy,
		}
		name := rendering.RouteTLSName(definition.Name, key, routeKey)
		if certificate := certificates[name]; certificate != nil {
			if len(certificate.DNSNames) > 0 {
				status.Domain = certificate.DNSNames[0]
			}
			status.Certificate = &CertificateInfo{
				Name:        name,
				SecretName:  certificate.SecretName,
				State:       certificateState(certificate, time.Now()),
				Reason:      certificate.Reason,
				Message:     certificate.Message,
				NotAfter:    certificate.NotAfter,
				RenewalTime: certificate.RenewalTime,
			}
		}
		routes = append(routes, status)
	}
	return routes
}

// certificateState derives the display state of one certificate.
func certificateState(certificate *module.CertificateStatus, now time.Time) string {
	switch {
	case !certificate.NotAfter.IsZero() && now.After(certificate.NotAfter):
		return "expired"
	case certificate.Ready:
		return "active"
	case certificate.FailedAttempts > 0:
		return "failing"
	case certificate.Issuing:
		return "issuing"
	case !certificate.NotAfter.IsZero():
		// Not ready with a valid certificate on hand: a renewal that is
		// not succeeding, which failing describes better than pending.
		return "failing"
	default:
		return "pending"
	}
}

// routeDomain resolves a route domain against the pinned variables and
// falls back to the placeholder rendering when a variable is missing.
func routeDomain(expression compiler.Expression, variables map[string]string) string {
	if domain, err := compiler.ResolveExpression(expression, variables); err == nil {
		return domain
	}
	return expressionDisplay(expression)
}

// expressionDisplay renders a compiled expression for status output:
// literals verbatim, everything else as its ${NAME} placeholder.
func expressionDisplay(expression compiler.Expression) string {
	var builder strings.Builder
	for _, part := range expression.Parts {
		if part.Kind == "literal" {
			builder.WriteString(part.Value)
			continue
		}
		builder.WriteString("${" + part.Name + "}")
	}
	return builder.String()
}

// servingColor is the blue-green color the application's live Service
// selects; without a Service the desired color stands in (nothing to switch).
func servingColor(snapshot observe.Snapshot, service, desired string) string {
	if svc := liveObject(snapshot, service, module.KindService); svc != nil && svc.Selector != nil {
		return svc.Color
	}
	return desired
}

func podsFor(snapshot observe.Snapshot, service string) []PodInfo {
	var pods []PodInfo
	serving := ""
	hasService := false
	if svc := liveObject(snapshot, service, module.KindService); svc != nil && svc.Selector != nil {
		serving, hasService = svc.Color, true
	}
	for _, obj := range snapshot.Objects {
		if obj.Kind != module.KindPod || obj.Service != service || obj.Pod == nil {
			continue
		}
		pods = append(pods, PodInfo{
			Name:     obj.Name,
			Node:     obj.Pod.Node,
			Phase:    obj.Pod.Phase,
			Ready:    obj.Pod.Ready,
			Restarts: obj.Pod.Restarts,
			Reason:   obj.Pod.Reason,
			Started:  obj.Pod.Started,
			Color:    obj.Color,
			Serving:  !hasService || obj.Color == serving,
		})
	}
	return pods
}
