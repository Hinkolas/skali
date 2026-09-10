// Package kubernetes deterministically renders a compiled Skali definition
// into Kubernetes API objects. Rendering is pure; applying and observing these
// objects belongs to the later reconciliation layer.
package kubernetes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/values"
)

type Options struct {
	Namespace             string
	EnvironmentSecretName string
	Variables             map[string]string
	BuildImages           map[string]string

	// AppPlatforms lists, per application key, the platforms its image
	// runs on (from the revision artifact: a build's target platforms or
	// an image source's declared platforms). On managed clusters the
	// Deployment and release Job derive a required kubernetes.io/arch
	// affinity from it so pods never schedule onto a node that cannot run
	// the image. A missing entry means unknown and renders no constraint.
	AppPlatforms map[string][]string

	// EnvironmentID and RevisionChecksum stamp the identity labels used by
	// observation and pruning. Both are optional so offline rendering (the
	// CLI compile preview) stays possible without an environment.
	EnvironmentID    string
	RevisionChecksum string

	// RestartedAt is the environment target's restart stamp (RFC3339, UTC);
	// non-empty values become a pod-template annotation on application
	// workloads so a forced deployment rolls them even when the revision is
	// unchanged. Stateful services never carry it.
	RestartedAt string

	// AppRestartedAt carries per-application restart stamps (RFC3339, UTC)
	// keyed by application key; a service restart stamps one application so
	// only its workload rolls. Per application the later of RestartedAt and
	// its own stamp wins, so environment-wide force and service restarts
	// compose.
	AppRestartedAt map[string]string

	// SecretVersions carries the stored generation per secret variable for
	// the per-application values identity; optional so offline rendering
	// stays possible.
	SecretVersions map[string]int

	// ProgressDeadlineSeconds mirrors the kernel rollout deadline onto
	// rendered Deployments; zero omits the field (offline rendering, the
	// Kubernetes default applies).
	ProgressDeadlineSeconds int64

	// ManagedCluster enables capability placement on Skali-labeled nodes.
	ManagedCluster bool

	// StorageClass names the storage class application volume claims
	// provision on (the skali-app Longhorn class on managed clusters).
	// Empty keeps the cluster default, so offline rendering and dev
	// clusters stay byte-identical to a render before the field existed.
	StorageClass string

	// Certificates enables TLS issuance: routes not opting out render a
	// websecure IngressRoute, an explicit cert-manager Certificate, and a
	// plain-HTTP companion (redirecting on `automatic`). False keeps every
	// route on the plain web entrypoint; local installations run no
	// cert-manager, and offline rendering has no way to know.
	Certificates bool

	// Intercepts marks applications served by a local dev process on the
	// host instead of a Deployment: application key to rendered service
	// port name to host port. Intercepted applications render their
	// Service without a selector plus a managed EndpointSlice targeting
	// InterceptHostIP, keep their IngressRoutes and PVCs, and render no
	// Deployment, HPA, or release Job.
	Intercepts map[string]map[string]int32
	// InterceptHostIP is the address in-cluster traffic uses to reach the
	// host (host.k3d.internal resolved to an IP; kube-proxy ignores FQDN
	// endpoints). Required when Intercepts is non-empty.
	InterceptHostIP string

	// TrafficColors pins, per blue-green application key, the color its
	// Service selects while a switch is pending. An absent key selects the
	// rendered color (first deploy, converged, or the switch itself); a
	// present key selects that color, and the empty string keeps the
	// selector uncolored so a legacy or rolling Deployment keeps serving
	// until its replacement is ready. Offline rendering leaves it nil.
	TrafficColors map[string]string
	// PendingReplicas sizes, per autoscaled blue-green application key,
	// the color that is waiting for traffic: the autoscaler steers the
	// serving color, so the kernel copies its count onto the pending one.
	// Ignored unless TrafficColors pins that application elsewhere.
	PendingReplicas map[string]int32

	// PriorityClassName is the PriorityClass every application pod
	// (Deployments and release Jobs) names, derived from the environment's
	// priority setting and rendered live like RestartedAt rather than
	// stored in the revision. Empty renders no field, so offline rendering
	// and clusters without the bundle classes stay clean.
	PriorityClassName string
}

// revisionHistoryLimit bounds retained ReplicaSets. Rollback re-renders old
// revisions from storage, never kubectl rollout undo, so history is only
// for manual inspection of recent rollouts.
const revisionHistoryLimit = 3

func Render(result *compiler.Result, options Options) ([]runtime.Object, error) {
	if options.Namespace == "" {
		return nil, fmt.Errorf("kubernetes namespace is required")
	}
	if options.EnvironmentSecretName == "" {
		options.EnvironmentSecretName = EnvironmentSecretName
	}
	// Orphaned variables are ignored by design; only missing required
	// runtime values block a render.
	if _, missing, _ := values.Conform(result.Definition.RequiredVariables, options.Variables); len(missing) > 0 {
		return nil, fmt.Errorf("missing project variables: %s", strings.Join(missing, ", "))
	}
	if _, err := compiler.ResolveRoutes(result.Definition, options.Variables); err != nil {
		return nil, err
	}
	var objects []runtime.Object
	// One shared redirect Middleware serves every `tls: automatic` route of
	// the environment; it carries no service label, so it joins pruning and
	// teardown without ever entering a service snapshot.
	if options.Certificates && needsRedirect(result.Definition) {
		labels := map[string]string{
			LabelManaged: "true",
			LabelProject: result.Definition.Name,
		}
		if options.EnvironmentID != "" {
			labels[LabelEnvironment] = options.EnvironmentID
		}
		objects = append(objects, edge.RedirectMiddleware(options.Namespace, labels))
	}
	for _, key := range utils.SortedKeys(result.Definition.Applications) {
		rendered, err := renderApplication(result.Definition, key, options)
		if err != nil {
			return nil, fmt.Errorf("render application %s: %w", key, err)
		}
		objects = append(objects, rendered...)
	}
	if err := ValidateObjects(objects); err != nil {
		return nil, err
	}
	return objects, nil
}

// needsRedirect reports whether any route wants the HTTP-to-HTTPS redirect.
func needsRedirect(project compiler.ProjectDefinition) bool {
	for _, application := range project.Applications {
		for _, route := range application.Routes {
			if route.TLS == "automatic" {
				return true
			}
		}
	}
	return false
}

// RouteTLSName names one route's Certificate and the Secret it issues into,
// composed exactly as the renderer composes it so the app module can match
// observed Certificates against manifest routes.
func RouteTLSName(projectName, applicationKey, routeKey string) string {
	return objectName("tls", projectName, applicationKey, routeKey)
}

func renderApplication(project compiler.ProjectDefinition, key string, options Options) ([]runtime.Object, error) {
	application := project.Applications[key]
	name := ApplicationName(project.Name, key)
	// Selector labels are baked into immutable Deployment and Service
	// selectors: stable across revisions by contract (blue-green adds the
	// per-Deployment color on top). Object labels add the
	// environment/service/revision identity for observation and pruning.
	selectorLabels, labels := applicationLabels(project, key, options)
	hostPorts, intercepted := options.Intercepts[key]
	template, image, err := renderPodTemplate(project, key, labels, selectorLabels, options)
	if err != nil {
		if intercepted && errors.Is(err, errNoPreparedImage) {
			// An intercepted build application renders no workload, so a
			// missing image is not an error for it.
			image = ""
		} else {
			return nil, err
		}
	}

	var objects []runtime.Object
	for _, volumeKey := range utils.SortedKeys(application.Volumes) {
		volume := application.Volumes[volumeKey]
		claim := &corev1.PersistentVolumeClaim{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      VolumeClaimName(project.Name, key, volumeKey),
				Namespace: options.Namespace,
				Labels:    maps.Clone(labels),
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: *resource.NewQuantity(volume.SizeBytes, resource.DecimalSI),
				}},
			},
		}
		if options.StorageClass != "" {
			claim.Spec.StorageClassName = &options.StorageClass
		}
		objects = append(objects, claim)
	}

	if len(application.Deployment.ReleaseCommand.Command) > 0 && !intercepted {
		objects = append(objects, renderReleaseJob(project, key, name, image, labels, options))
	}

	// Blue-green applications run one Deployment per color: the color is the
	// pod template's own hash, so an unchanged template keeps its Deployment
	// and only a real change starts a new one beside it. The Service keeps
	// the application's stable name and selects exactly one color.
	blueGreen := application.Deployment.Rollout.Strategy == compiler.StrategyBlueGreen && !intercepted
	color := ""
	deploymentName := name
	deploymentLabels := maps.Clone(labels)
	deploySelector := maps.Clone(selectorLabels)
	if blueGreen {
		color = TemplateColor(template)
		deploymentName = ColoredApplicationName(project.Name, key, color)
		deploymentLabels[LabelColor] = color
		deploySelector[LabelColor] = color
		template.Labels[LabelColor] = color
		for index := range template.Spec.TopologySpreadConstraints {
			template.Spec.TopologySpreadConstraints[index].LabelSelector = &metav1.LabelSelector{MatchLabels: maps.Clone(deploySelector)}
		}
	}
	// serviceColor is the color the Service selects: the rendered color
	// unless the kernel pins traffic elsewhere while a switch is pending.
	serviceColor := color
	if pinned, held := options.TrafficColors[key]; held {
		serviceColor = pinned
	}
	servingDeployment := deploymentName
	if serviceColor != color {
		servingDeployment = name
		if serviceColor != "" {
			servingDeployment = ColoredApplicationName(project.Name, key, serviceColor)
		}
	}

	autoscalingEnabled := application.Scaling.MaxReplicas > application.Scaling.MinReplicas && !intercepted
	var replicas *int32
	if !autoscalingEnabled {
		replicas = new(int32(application.Scaling.MinReplicas))
	} else if pending, ok := options.PendingReplicas[key]; ok && servingDeployment != deploymentName {
		// A color waiting for traffic is sized by the kernel to the serving
		// count; the autoscaler still steers the serving color.
		replicas = new(pending)
	}
	if !intercepted {
		strategy, err := renderStrategy(application.Deployment.Rollout)
		if err != nil {
			return nil, fmt.Errorf("application %s: %w", key, err)
		}
		deployment := &appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      deploymentName,
				Namespace: options.Namespace,
				Labels:    deploymentLabels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas:             replicas,
				RevisionHistoryLimit: new(int32(revisionHistoryLimit)),
				Selector:             &metav1.LabelSelector{MatchLabels: deploySelector},
				Strategy:             strategy,
				Template:             template,
			},
		}
		// The manifest's rollout timeout bounds the controller's progress
		// deadline per application; the kernel-wide deadline is the default.
		deadline := options.ProgressDeadlineSeconds
		if timeout := application.Deployment.Rollout.TimeoutMillis; timeout > 0 {
			deadline = (timeout + 999) / 1000
		}
		if deadline > 0 {
			deployment.Spec.ProgressDeadlineSeconds = new(int32(deadline))
		}
		objects = append(objects, deployment)
	}

	servicePorts := renderServicePorts(application)
	if len(servicePorts) > 0 {
		// An intercepted application's Service drops its selector: kube-proxy
		// then routes it by the managed EndpointSlice below, which points at
		// the local dev process on the host.
		serviceSelector := maps.Clone(selectorLabels)
		if serviceColor != "" {
			serviceSelector[LabelColor] = serviceColor
		}
		if intercepted {
			serviceSelector = nil
		}
		objects = append(objects, &corev1.Service{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: options.Namespace,
				Labels:    maps.Clone(labels),
			},
			Spec: corev1.ServiceSpec{
				Selector: serviceSelector,
				Ports:    servicePorts,
			},
		})
		if intercepted {
			slice, err := renderInterceptSlice(objectName("intercept", project.Name, key), name, options.Namespace, labels, servicePorts, hostPorts, options.InterceptHostIP)
			if err != nil {
				return nil, err
			}
			objects = append(objects, slice)
		}
	}

	// Every rendered route uses the managed Traefik edge, addressed through
	// its IngressRoute CRD so the balancing strategy and the HTTP redirect
	// are first-class. Certificates render only on installations that run
	// cert-manager; without them every route serves plain HTTP, exactly the
	// local dev contract.
	for _, routeKey := range utils.SortedKeys(application.Routes) {
		route := application.Routes[routeKey]
		domain, err := compiler.ResolveExpression(route.Domain, options.Variables)
		if err != nil {
			return nil, fmt.Errorf("route %s domain: %w", routeKey, err)
		}
		domain, err = edge.CanonicalDomain(domain)
		if err != nil {
			return nil, fmt.Errorf("route %s domain: %w", routeKey, err)
		}
		match := edge.HostMatch(domain, route.Path)
		backend := edge.Service{Name: name}
		if route.Port.Name != "" {
			backend.PortName = route.Port.Name
		} else {
			backend.PortNumber = route.Port.Number
		}
		if route.Strategy == "least-requests" {
			backend.Strategy = edge.StrategyP2C
		}
		if options.Certificates && route.TLS != "disabled" {
			secretName := RouteTLSName(project.Name, key, routeKey)
			objects = append(objects, edge.IngressRoute(options.Namespace, RouteName(project.Name, key, routeKey, "primary"),
				maps.Clone(labels), []string{edge.EntryPointWebSecure},
				[]edge.Route{{Match: match, Service: backend}}, secretName))
			// The plain-HTTP router redirects on `automatic` and serves the
			// backend directly on `optional`, the per-route escape hatch for
			// consumers that cannot follow redirects.
			httpRoute := edge.Route{Match: match, Service: backend}
			if route.TLS == "automatic" {
				httpRoute.Middlewares = []string{edge.RedirectMiddlewareName}
			}
			objects = append(objects, edge.IngressRoute(options.Namespace, RouteName(project.Name, key, routeKey, "http"),
				maps.Clone(labels), []string{edge.EntryPointWeb},
				[]edge.Route{httpRoute}, ""))
			objects = append(objects, edge.Certificate(options.Namespace, secretName, domain, maps.Clone(labels)))
		} else {
			objects = append(objects, edge.IngressRoute(options.Namespace, RouteName(project.Name, key, routeKey, "primary"),
				maps.Clone(labels), []string{edge.EntryPointWeb},
				[]edge.Route{{Match: match, Service: backend}}, ""))
		}
	}

	// Record canonical hosts for conservative, fresh-read claim retirement.
	resolved, err := compiler.ResolveRoutes(project, options.Variables)
	if err != nil {
		return nil, err
	}
	for _, route := range resolved {
		if route.Application != key {
			continue
		}
		for _, obj := range objects {
			m, _ := meta.Accessor(obj)
			if obj.GetObjectKind().GroupVersionKind() == edge.IngressRouteGVK && (m.GetName() == RouteName(project.Name, key, route.Key, "primary") || m.GetName() == RouteName(project.Name, key, route.Key, "http")) {
				m.SetAnnotations(map[string]string{"skali.dev/route-hostname": route.Domain})
			}
		}
	}

	if autoscalingEnabled {
		target := int32(application.Scaling.CPUTargetUtilization)
		objects = append(objects, &autoscalingv2.HorizontalPodAutoscaler{
			TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: options.Namespace,
				Labels:    maps.Clone(labels),
			},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				// The autoscaler steers the Deployment that carries traffic;
				// a pending color is sized by the kernel until the switch.
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       servingDeployment,
				},
				MinReplicas: new(int32(application.Scaling.MinReplicas)),
				MaxReplicas: int32(application.Scaling.MaxReplicas),
				Metrics: []autoscalingv2.MetricSpec{{
					Type: autoscalingv2.ResourceMetricSourceType,
					Resource: &autoscalingv2.ResourceMetricSource{
						Name: corev1.ResourceCPU,
						Target: autoscalingv2.MetricTarget{
							Type:               autoscalingv2.UtilizationMetricType,
							AverageUtilization: &target,
						},
					},
				}},
			},
		})
	}
	return objects, nil
}

// errNoPreparedImage reports a build-sourced application rendered without
// its prepared image. Intercepted applications tolerate it (they render no
// workload); everything else fails the render.
var errNoPreparedImage = errors.New("build source has no prepared image")

// renderPodTemplate builds an application's pod template: the one input the
// blue-green color is derived from. It carries everything that must roll
// the application when it changes (image, environment, probes, resources,
// restart stamp, values identity, priority, placement) and nothing that
// must not (the revision label). Spreading is rendered over the base
// selector; blue-green callers narrow it to their color after hashing.
func renderPodTemplate(project compiler.ProjectDefinition, key string, labels, selectorLabels map[string]string,
	options Options) (corev1.PodTemplateSpec, string, error) {
	application := project.Applications[key]
	image := application.Source.Image
	if application.Source.Kind == "build" {
		image = options.BuildImages[key]
		if image == "" {
			return corev1.PodTemplateSpec{}, "", errNoPreparedImage
		}
	}

	container := corev1.Container{
		Name:            key,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args:            append([]string(nil), application.Command...),
		Env:             renderEnvironment(key, application.Environment, options.EnvironmentSecretName),
		Resources:       renderResources(application.Resources),
	}
	for _, portKey := range utils.SortedKeys(application.Ports) {
		port := application.Ports[portKey]
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Name:          portKey,
			ContainerPort: int32(port.Port),
			Protocol:      kubernetesProtocol(port.Protocol),
		})
	}
	container.StartupProbe = renderProbe(application.Health.Startup)
	container.ReadinessProbe = renderProbe(application.Health.Readiness)
	container.LivenessProbe = renderProbe(application.Health.Liveness)
	for _, volumeKey := range utils.SortedKeys(application.Volumes) {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      volumeKey,
			MountPath: application.Volumes[volumeKey].MountPath,
		})
	}

	graceSeconds := int64(time.Duration(application.Shutdown.GracePeriodMillis) * time.Millisecond / time.Second)
	// The revision label stays off the pod template: it would roll every
	// application on every revision. The template instead carries a values
	// identity so exactly the applications whose referenced values changed
	// roll, and everything else rolls only on a real spec change.
	templateLabels := maps.Clone(labels)
	delete(templateLabels, LabelRevision)
	template := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      templateLabels,
			Annotations: templateAnnotations(options, key, valuesIdentity(application, options)),
		},
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: &graceSeconds,
			Containers:                    []corev1.Container{container},
		},
	}
	if options.PriorityClassName != "" {
		template.Spec.PriorityClassName = options.PriorityClassName
	}
	if options.ManagedCluster {
		template.Spec.NodeSelector = map[string]string{
			layout.CapabilityLabel(layout.CapabilityApplication): layout.CapabilityLabelValue,
		}
	}
	template.Spec.Affinity = renderArchAffinity(options, key)
	for _, volumeKey := range utils.SortedKeys(application.Volumes) {
		template.Spec.Volumes = append(template.Spec.Volumes, corev1.Volume{
			Name: volumeKey,
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: VolumeClaimName(project.Name, key, volumeKey),
			}},
		})
	}
	if constraint := renderSpread(selectorLabels, application.Placement); constraint != nil {
		template.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{*constraint}
	}
	return template, image, nil
}

// TemplateColor derives a blue-green color from a pod template: the first
// ten hex characters of the template's JSON hash. Identical templates share
// a color (and a Deployment); any change starts a new one.
func TemplateColor(template corev1.PodTemplateSpec) string {
	data, err := json.Marshal(template)
	if err != nil {
		// PodTemplateSpec is a plain API struct; marshalling cannot fail.
		panic("render: marshal pod template: " + err.Error())
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:10]
}

// ApplicationColors reports the desired color of every blue-green
// application that renders a workload, keyed by application key. It is the
// kernel's input for the traffic decision and shares the template builder
// with Render, so the two can never disagree.
func ApplicationColors(result *compiler.Result, options Options) (map[string]string, error) {
	if options.EnvironmentSecretName == "" {
		options.EnvironmentSecretName = EnvironmentSecretName
	}
	colors := map[string]string{}
	project := result.Definition
	for _, key := range utils.SortedKeys(project.Applications) {
		application := project.Applications[key]
		if application.Deployment.Rollout.Strategy != compiler.StrategyBlueGreen {
			continue
		}
		if _, intercepted := options.Intercepts[key]; intercepted {
			continue
		}
		selectorLabels, labels := applicationLabels(project, key, options)
		template, _, err := renderPodTemplate(project, key, labels, selectorLabels, options)
		if err != nil {
			return nil, fmt.Errorf("application %s: %w", key, err)
		}
		colors[key] = TemplateColor(template)
	}
	return colors, nil
}

// applicationLabels returns an application's immutable selector labels and
// its full object labels for one render.
func applicationLabels(project compiler.ProjectDefinition, key string, options Options) (selectorLabels, labels map[string]string) {
	name := ApplicationName(project.Name, key)
	selectorLabels = map[string]string{
		"app.kubernetes.io/name": name,
		LabelManaged:             "true",
		LabelProject:             project.Name,
		LabelApplication:         key,
	}
	labels = maps.Clone(selectorLabels)
	labels[LabelService] = key
	if options.EnvironmentID != "" {
		labels[LabelEnvironment] = options.EnvironmentID
	}
	if options.RevisionChecksum != "" {
		labels[LabelRevision] = RevisionLabelValue(options.RevisionChecksum)
	}
	return selectorLabels, labels
}

// DefaultReleaseTimeout bounds a release command whose manifest declares no
// explicit timeout.
const DefaultReleaseTimeout = 10 * time.Minute

// ReleaseJobName names one application's release Job for one revision. The
// revision in the name makes a new release create a fresh Job while a
// re-converging pass finds the completed one; Jobs are immutable, so the
// name is the release identity.
func ReleaseJobName(projectName, key, revisionChecksum string) string {
	return objectName("release", projectName, key, revisionChecksum)
}

// ReleaseServiceIdentity is the LabelService value of release-command pods:
// distinct from the application's bare key so release members never match
// the application's immutable selectors, health evaluation, or member
// listings.
func ReleaseServiceIdentity(key string) string { return "release." + key }

// IsReleaseServiceIdentity reports whether a LabelService value names the
// release plane rather than an application service.
func IsReleaseServiceIdentity(service string) bool { return strings.HasPrefix(service, "release.") }

// renderReleaseJob renders the application's release command as a
// single-attempt Job: the reconciler runs it to completion before the
// workload of a new release rolls forward. One attempt only (no backoff):
// a failed release fails the deployment instead of retrying a command
// whose partial effects are unknown. The Job's own deadline enforces the
// manifest timeout, so a hung command fails visibly rather than pending
// forever.
func renderReleaseJob(project compiler.ProjectDefinition, key, name, image string,
	labels map[string]string, options Options) *batchv1.Job {
	application := project.Applications[key]
	timeout := time.Duration(application.Deployment.ReleaseCommand.TimeoutMillis) * time.Millisecond
	if timeout <= 0 {
		timeout = DefaultReleaseTimeout
	}
	deadlineSeconds := int64(timeout / time.Second)

	// The Job object carries the service identity like every other object of
	// the application; the pod template does not. Release pods with the
	// application's bare key and name label would match its immutable
	// Service/Deployment selectors and join its health evaluation and member
	// listings.
	podLabels := maps.Clone(labels)
	podLabels["app.kubernetes.io/name"] = name + "-release"
	podLabels[LabelService] = ReleaseServiceIdentity(key)

	container := corev1.Container{
		Name:            "release",
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args:            append([]string(nil), application.Deployment.ReleaseCommand.Command...),
		Env:             renderEnvironment(key, application.Environment, options.EnvironmentSecretName),
		Resources:       renderResources(application.Resources),
	}
	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ReleaseJobName(project.Name, key, options.RevisionChecksum),
			Namespace: options.Namespace,
			Labels:    maps.Clone(labels),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          new(int32(0)),
			ActiveDeadlineSeconds: &deadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{container},
				},
			},
		},
	}
	if options.PriorityClassName != "" {
		job.Spec.Template.Spec.PriorityClassName = options.PriorityClassName
	}
	if options.ManagedCluster {
		job.Spec.Template.Spec.NodeSelector = map[string]string{
			layout.CapabilityLabel(layout.CapabilityApplication): layout.CapabilityLabelValue,
		}
	}
	job.Spec.Template.Spec.Affinity = renderArchAffinity(options, key)
	return job
}

// renderEnvironment binds one application's environment variables: service
// outputs reference the substrate-owned output Secrets, anything touching a
// project value references its composed entry in the environment Secret
// (see EnvironmentSecretData), and pure literals inline.
func renderEnvironment(applicationKey string, environment map[string]compiler.Expression, environmentSecret string) []corev1.EnvVar {
	result := make([]corev1.EnvVar, 0, len(environment))
	for _, name := range utils.SortedKeys(environment) {
		expression := environment[name]
		variable := corev1.EnvVar{Name: name}
		if part, ok := expression.ServiceOutput(); ok {
			variable.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: OutputSecretName(part.Collection, part.Service)},
				Key:                  part.Output,
			}}
		} else if expression.HasProjectVariables() {
			variable.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: environmentSecret},
				Key:                  EnvironmentSecretKey(applicationKey, name),
			}}
		} else {
			variable.Value = expression.Literal()
		}
		result = append(result, variable)
	}
	return result
}

func renderResources(resources compiler.Resources) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: resourceList(resources.Requests),
		Limits:   resourceList(resources.Limits),
	}
}

func resourceList(values compiler.ResourceValues) corev1.ResourceList {
	result := corev1.ResourceList{}
	if values.MilliCPU > 0 {
		result[corev1.ResourceCPU] = *resource.NewMilliQuantity(values.MilliCPU, resource.DecimalSI)
	}
	if values.MemoryBytes > 0 {
		result[corev1.ResourceMemory] = *resource.NewQuantity(values.MemoryBytes, resource.DecimalSI)
	}
	if values.TemporaryStorageBytes > 0 {
		result[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(values.TemporaryStorageBytes, resource.DecimalSI)
	}
	return result
}

func renderProbe(probe compiler.Probe) *corev1.Probe {
	if probe.HTTP.Path == "" {
		return nil
	}
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: probe.HTTP.Path,
			Port: targetPort(probe.HTTP.Port),
		}},
		PeriodSeconds:    durationSeconds(probe.IntervalMillis),
		TimeoutSeconds:   durationSeconds(probe.TimeoutMillis),
		FailureThreshold: int32(probe.FailureThreshold),
	}
}

func renderServicePorts(application compiler.Application) []corev1.ServicePort {
	ports := make([]corev1.ServicePort, 0, len(application.Ports)+len(application.Routes))
	numbers := make(map[int]struct{})
	for _, name := range utils.SortedKeys(application.Ports) {
		port := application.Ports[name]
		numbers[port.Port] = struct{}{}
		appProtocol := port.Protocol
		ports = append(ports, corev1.ServicePort{
			Name:        name,
			Port:        int32(port.Port),
			TargetPort:  intstr.FromString(name),
			Protocol:    kubernetesProtocol(port.Protocol),
			AppProtocol: &appProtocol,
		})
	}
	for _, name := range utils.SortedKeys(application.Routes) {
		target := application.Routes[name].Port
		if target.Number == 0 {
			continue
		}
		if _, exists := numbers[target.Number]; exists {
			continue
		}
		numbers[target.Number] = struct{}{}
		ports = append(ports, corev1.ServicePort{
			Name:       routePortName(name),
			Port:       int32(target.Number),
			TargetPort: intstr.FromInt32(int32(target.Number)),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	return ports
}

// renderStrategy maps the compiled rollout onto the Deployment controller's
// strategy. A blue-green color's template never changes by construction
// (a changed template is a new Deployment), so its controller strategy is
// moot and renders as Recreate, which states that plainly. An unknown
// strategy fails the render: a daemon older than the manifest's vocabulary
// must refuse instead of applying an invalid rolling update.
func renderStrategy(rollout compiler.Rollout) (appsv1.DeploymentStrategy, error) {
	switch rollout.Strategy {
	case compiler.StrategyRecreate, compiler.StrategyBlueGreen:
		return appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, nil
	case compiler.StrategyRolling:
		return appsv1.DeploymentStrategy{
			Type: appsv1.RollingUpdateDeploymentStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxUnavailable: new(intstr.FromInt32(int32(rollout.MaxUnavailable))),
				MaxSurge:       new(intstr.FromInt32(int32(rollout.MaxSurge))),
			},
		}, nil
	default:
		return appsv1.DeploymentStrategy{}, fmt.Errorf("unsupported rollout strategy %q", rollout.Strategy)
	}
}

func renderSpread(labels map[string]string, placement compiler.Placement) *corev1.TopologySpreadConstraint {
	topologyKey := corev1.LabelHostname
	if placement.SpreadAcross == "zones" {
		topologyKey = corev1.LabelTopologyZone
	}
	action := corev1.ScheduleAnyway
	if placement.Enforcement == "required" {
		action = corev1.DoNotSchedule
	}
	constraint := &corev1.TopologySpreadConstraint{
		MaxSkew:           1,
		TopologyKey:       topologyKey,
		WhenUnsatisfiable: action,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: maps.Clone(labels)},
	}
	// Kubernetes only permits minDomains with DoNotSchedule. Preferred spread
	// still balances across every available topology domain, while Skali keeps
	// the requested minimum in its IR for health/diagnostic projections.
	if placement.Minimum > 0 && action == corev1.DoNotSchedule {
		constraint.MinDomains = new(int32(placement.Minimum))
	}
	return constraint
}

// renderArchAffinity pins an application's pods to nodes whose
// architecture its image was built for, derived from the revision
// artifact's platform set (never a policy knob of its own). Nil outside
// managed clusters and for applications with an unknown platform set, so
// dev clusters and pre-platform revisions render byte-identically.
func renderArchAffinity(options Options, key string) *corev1.Affinity {
	platforms := options.AppPlatforms[key]
	if !options.ManagedCluster || len(platforms) == 0 {
		return nil
	}
	archs := make([]string, 0, len(platforms))
	for _, platform := range platforms {
		arch, ok := strings.CutPrefix(platform, "linux/")
		if !ok || arch == "" {
			continue
		}
		archs = append(archs, arch)
	}
	if len(archs) == 0 {
		return nil
	}
	slices.Sort(archs)
	return &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      corev1.LabelArchStable,
						Operator: corev1.NodeSelectorOpIn,
						Values:   archs,
					}},
				}},
			},
		},
	}
}

func targetPort(target compiler.PortTarget) intstr.IntOrString {
	if target.Name != "" {
		return intstr.FromString(target.Name)
	}
	return intstr.FromInt32(int32(target.Number))
}

func kubernetesProtocol(protocol string) corev1.Protocol {
	if protocol == "udp" {
		return corev1.ProtocolUDP
	}
	return corev1.ProtocolTCP
}

func durationSeconds(milliseconds int64) int32 {
	seconds := (milliseconds + 999) / 1000
	if seconds < 1 {
		return 1
	}
	return int32(seconds)
}

// VolumeClaimName is the PVC name of one application volume, for callers
// outside rendering (the backup engine mounts the PVC into its Jobs). It
// hashes the original project/application/volume tuple exactly once.
func VolumeClaimName(project, application, volume string) string {
	return objectName("volume", project, application, volume)
}

// ApplicationName is shared by workloads and their Service references.
func ApplicationName(project, application string) string {
	return objectName("app", project, application)
}

// ColoredApplicationName names one blue-green color's Deployment. The
// Service, HPA, and routes keep ApplicationName; only workloads are colored.
func ColoredApplicationName(project, application, color string) string {
	return objectName("app", project, application, color)
}

func RouteName(project, application, route, variant string) string {
	return objectName("route", project, application, route, variant)
}

func objectName(parts ...string) string {
	// JSON's array encoding preserves component boundaries, including hyphens.
	encoded, _ := json.Marshal(parts)
	sum := sha256.Sum256(encoded)
	suffix := hex.EncodeToString(sum[:16])
	prefix := strings.Trim(strings.ToLower(strings.Join(parts, "-")), "-")
	if len(prefix) > 30 {
		prefix = strings.TrimRight(prefix[:30], "-")
	}
	return prefix + "-" + suffix
}

// valuesIdentity hashes the stored generations of exactly the project
// variables this application's environment references, whole-value or
// composed: NAME=v<version> lines, sorted, sha256[:8]. Plaintext never
// enters the hash. Empty when the environment references no variables.
// Excluded by design: service outputs (their Secrets rotate through their
// own lifecycle), route domains and paths (edge-only, they resolve into
// IngressRoutes; hashing them would roll pods on edge-only changes),
// command, probe, and mount paths (they
// resolve into the pod template, so a change rolls naturally), and build
// arguments (they flow into the image digest).
func valuesIdentity(application compiler.Application, options Options) string {
	referenced := map[string]bool{}
	for _, expression := range application.Environment {
		for _, part := range expression.Parts {
			if part.Kind == "project_variable" {
				referenced[part.Name] = true
			}
		}
	}
	if len(referenced) == 0 {
		return ""
	}
	lines := make([]string, 0, len(referenced))
	for _, name := range utils.SortedKeys(referenced) {
		lines = append(lines, fmt.Sprintf("%s=v%d", name, options.SecretVersions[name]))
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:8])
}

// templateAnnotations carries the restart stamp and the values identity
// into application pod templates; nil (no annotations at all) when neither
// applies, so existing objects do not change shape. Both stamp sources are
// UTC RFC3339, so the lexicographic maximum is the later stamp.
func templateAnnotations(options Options, key, valuesHash string) map[string]string {
	restartedAt := max(options.RestartedAt, options.AppRestartedAt[key])
	if restartedAt == "" && valuesHash == "" {
		return nil
	}
	annotations := map[string]string{}
	if restartedAt != "" {
		annotations[AnnotationRestartedAt] = restartedAt
	}
	if valuesHash != "" {
		annotations[AnnotationValuesHash] = valuesHash
	}
	return annotations
}

// renderInterceptSlice is the routing half of an intercept: the managed
// EndpointSlice that sends the selectorless Service's traffic to the local
// dev process on the host. The service-name label is what kube-proxy joins
// on; the managed-by label keeps the kube endpointslice controller from
// garbage-collecting a slice it does not own.
func renderInterceptSlice(sliceName, serviceName, namespace string, labels map[string]string,
	servicePorts []corev1.ServicePort, hostPorts map[string]int32, hostIP string) (*discoveryv1.EndpointSlice, error) {
	if hostIP == "" {
		return nil, fmt.Errorf("intercepted application requires the resolved host gateway address")
	}
	ports := make([]discoveryv1.EndpointPort, 0, len(servicePorts))
	for _, servicePort := range servicePorts {
		hostPort, ok := hostPorts[servicePort.Name]
		if !ok {
			return nil, fmt.Errorf("intercept declares no host port for service port %s", servicePort.Name)
		}
		name := servicePort.Name
		port := hostPort
		protocol := servicePort.Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		ports = append(ports, discoveryv1.EndpointPort{
			Name:     &name,
			Port:     &port,
			Protocol: &protocol,
		})
	}
	sliceLabels := maps.Clone(labels)
	sliceLabels["kubernetes.io/service-name"] = serviceName
	sliceLabels[LabelEndpointSliceManagedBy] = EndpointSliceManagedBySkali
	ready := true
	return &discoveryv1.EndpointSlice{
		TypeMeta: metav1.TypeMeta{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      sliceName,
			Namespace: namespace,
			Labels:    sliceLabels,
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{hostIP},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
		Ports: ports,
	}, nil
}

func routePortName(name string) string {
	if len(name) <= 57 {
		return "route-" + name
	}
	return objectName("route-port", name)
}
