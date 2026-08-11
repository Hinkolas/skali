// Package kubernetes deterministically renders a compiled Skali definition
// into Kubernetes API objects. Rendering is pure; applying and observing these
// objects belongs to the later reconciliation layer.
package kubernetes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/values"
)

type Options struct {
	Namespace             string
	EnvironmentSecretName string
	Variables             map[string]string
	BuildImages           map[string]string

	// EnvironmentID and RevisionChecksum stamp the identity labels used by
	// observation and pruning. Both are optional so offline rendering (the
	// CLI compile preview) stays possible without an environment.
	EnvironmentID    string
	RevisionChecksum string

	// RestartedAt is the environment target's restart stamp (RFC3339);
	// non-empty values become a pod-template annotation on application
	// workloads so a forced deployment rolls them even when the revision is
	// unchanged. Stateful services never carry it.
	RestartedAt string

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

	// Intercepts marks applications served by a local dev process on the
	// host instead of a Deployment: application key to rendered service
	// port name to host port. Intercepted applications render their
	// Service without a selector plus a managed EndpointSlice targeting
	// InterceptHostIP, keep their Ingresses and PVCs, and render no
	// Deployment, HPA, or release Job.
	Intercepts map[string]map[string]int32
	// InterceptHostIP is the address in-cluster traffic uses to reach the
	// host (host.k3d.internal resolved to an IP; kube-proxy ignores FQDN
	// endpoints). Required when Intercepts is non-empty.
	InterceptHostIP string
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
	var objects []runtime.Object
	for _, key := range utils.SortedKeys(result.Definition.Applications) {
		rendered, err := renderApplication(result.Definition, key, options)
		if err != nil {
			return nil, fmt.Errorf("render application %s: %w", key, err)
		}
		objects = append(objects, rendered...)
	}
	return objects, nil
}

func renderApplication(project compiler.ProjectDefinition, key string, options Options) ([]runtime.Object, error) {
	application := project.Applications[key]
	name := objectName(project.Name, key)
	// Selector labels are baked into immutable Deployment and Service
	// selectors: stable across revisions by contract. Object labels add the
	// environment/service/revision identity for observation and pruning.
	selectorLabels := map[string]string{
		"app.kubernetes.io/name": name,
		LabelManaged:             "true",
		LabelProject:             project.Name,
		LabelApplication:         key,
	}
	labels := maps.Clone(selectorLabels)
	labels[LabelService] = key
	if options.EnvironmentID != "" {
		labels[LabelEnvironment] = options.EnvironmentID
	}
	if options.RevisionChecksum != "" {
		labels[LabelRevision] = RevisionLabelValue(options.RevisionChecksum)
	}
	hostPorts, intercepted := options.Intercepts[key]
	image := application.Source.Image
	if application.Source.Kind == "build" {
		image = options.BuildImages[key]
		if image == "" && !intercepted {
			return nil, fmt.Errorf("build source has no prepared image")
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

	var objects []runtime.Object
	for _, volumeKey := range utils.SortedKeys(application.Volumes) {
		volume := application.Volumes[volumeKey]
		claimName := objectName(name, volumeKey)
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      volumeKey,
			MountPath: volume.MountPath,
		})
		objects = append(objects, &corev1.PersistentVolumeClaim{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      claimName,
				Namespace: options.Namespace,
				Labels:    maps.Clone(labels),
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: *resource.NewQuantity(volume.SizeBytes, resource.DecimalSI),
				}},
			},
		})
	}

	if len(application.Deployment.ReleaseCommand.Command) > 0 && !intercepted {
		objects = append(objects, renderReleaseJob(project, key, name, image, labels, options))
	}

	autoscalingEnabled := application.Scaling.MaxReplicas > application.Scaling.MinReplicas && !intercepted
	var replicas *int32
	if !autoscalingEnabled {
		replicas = new(int32(application.Scaling.MinReplicas))
	}
	graceSeconds := int64(time.Duration(application.Shutdown.GracePeriodMillis) * time.Millisecond / time.Second)
	// The revision label stays off the pod template: it would roll every
	// application on every revision. The template instead carries a values
	// identity so exactly the applications whose referenced values changed
	// roll, and everything else rolls only on a real spec change.
	templateLabels := maps.Clone(labels)
	delete(templateLabels, LabelRevision)
	if !intercepted {
		deployment := &appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: options.Namespace,
				Labels:    maps.Clone(labels),
			},
			Spec: appsv1.DeploymentSpec{
				Replicas:             replicas,
				RevisionHistoryLimit: new(int32(revisionHistoryLimit)),
				Selector:             &metav1.LabelSelector{MatchLabels: maps.Clone(selectorLabels)},
				Strategy:             renderStrategy(application.Deployment.Rollout),
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels:      templateLabels,
						Annotations: templateAnnotations(options, valuesIdentity(application, options)),
					},
					Spec: corev1.PodSpec{
						TerminationGracePeriodSeconds: &graceSeconds,
						Containers:                    []corev1.Container{container},
					},
				},
			},
		}
		if options.ProgressDeadlineSeconds > 0 {
			deployment.Spec.ProgressDeadlineSeconds = new(int32(options.ProgressDeadlineSeconds))
		}
		if options.ManagedCluster {
			deployment.Spec.Template.Spec.NodeSelector = map[string]string{
				layout.CapabilityLabel(layout.CapabilityApplication): layout.CapabilityLabelValue,
			}
		}
		for _, volumeKey := range utils.SortedKeys(application.Volumes) {
			deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: volumeKey,
				VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: objectName(name, volumeKey),
				}},
			})
		}
		if constraint := renderSpread(selectorLabels, application.Placement); constraint != nil {
			deployment.Spec.Template.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{*constraint}
		}
		objects = append(objects, deployment)
	}

	servicePorts := renderServicePorts(application)
	if len(servicePorts) > 0 {
		// An intercepted application's Service drops its selector: kube-proxy
		// then routes it by the managed EndpointSlice below, which points at
		// the local dev process on the host.
		serviceSelector := maps.Clone(selectorLabels)
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
			slice, err := renderInterceptSlice(name, options.Namespace, labels, servicePorts, hostPorts, options.InterceptHostIP)
			if err != nil {
				return nil, err
			}
			objects = append(objects, slice)
		}
	}

	// Every rendered route uses the managed k3s edge.
	ingressClass := "traefik"
	for _, routeKey := range utils.SortedKeys(application.Routes) {
		route := application.Routes[routeKey]
		domain, err := compiler.ResolveExpression(route.Domain, options.Variables)
		if err != nil {
			return nil, fmt.Errorf("route %s domain: %w", routeKey, err)
		}
		pathType := networkingv1.PathTypePrefix
		ingress := &networkingv1.Ingress{
			TypeMeta: metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "Ingress"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      objectName(name, routeKey),
				Namespace: options.Namespace,
				Labels:    maps.Clone(labels),
			},
			Spec: networkingv1.IngressSpec{
				IngressClassName: new(ingressClass),
				Rules: []networkingv1.IngressRule{{
					Host: domain,
					IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     route.Path,
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
								Name: name,
								Port: ingressPort(route.Port),
							}},
						}},
					}},
				}},
			},
		}
		if route.TLS == "automatic" {
			ingress.Annotations = map[string]string{"cert-manager.io/cluster-issuer": "skali"}
			ingress.Spec.TLS = []networkingv1.IngressTLS{{
				Hosts:      []string{domain},
				SecretName: objectName(name, routeKey, "tls"),
			}}
		}
		objects = append(objects, ingress)
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
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       name,
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

// DefaultReleaseTimeout bounds a release command whose manifest declares no
// explicit timeout.
const DefaultReleaseTimeout = 10 * time.Minute

// ReleaseJobName names one application's release Job for one revision. The
// revision in the name makes a new release create a fresh Job while a
// re-converging pass finds the completed one; Jobs are immutable, so the
// name is the release identity.
func ReleaseJobName(projectName, key, revisionChecksum string) string {
	return objectName(projectName, key, "release", RevisionLabelValue(revisionChecksum))
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
	if options.ManagedCluster {
		job.Spec.Template.Spec.NodeSelector = map[string]string{
			layout.CapabilityLabel(layout.CapabilityApplication): layout.CapabilityLabelValue,
		}
	}
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
			Name:       objectName("route", name),
			Port:       int32(target.Number),
			TargetPort: intstr.FromInt32(int32(target.Number)),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	return ports
}

func renderStrategy(rollout compiler.Rollout) appsv1.DeploymentStrategy {
	if rollout.Strategy == "recreate" {
		return appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	}
	return appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxUnavailable: new(intstr.FromInt32(int32(rollout.MaxUnavailable))),
			MaxSurge:       new(intstr.FromInt32(int32(rollout.MaxSurge))),
		},
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

func ingressPort(target compiler.PortTarget) networkingv1.ServiceBackendPort {
	if target.Name != "" {
		return networkingv1.ServiceBackendPort{Name: target.Name}
	}
	return networkingv1.ServiceBackendPort{Number: int32(target.Number)}
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
// must mirror the render path exactly: the application object name first,
// then the volume key, each pass applying the length cap.
func VolumeClaimName(project, application, volume string) string {
	return objectName(objectName(project, application), volume)
}

func objectName(parts ...string) string {
	value := strings.ToLower(strings.Join(parts, "-"))
	value = strings.Trim(value, "-")
	if len(value) <= 63 {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	suffix := hex.EncodeToString(sum[:4])
	return strings.TrimRight(value[:54], "-") + "-" + suffix
}

// valuesIdentity hashes the stored generations of exactly the project
// variables this application's environment references, whole-value or
// composed: NAME=v<version> lines, sorted, sha256[:8]. Plaintext never
// enters the hash. Empty when the environment references no variables.
// Excluded by design: service outputs (their Secrets rotate through their
// own lifecycle), route domains and paths (Ingress-only; hashing them would
// roll pods on edge-only changes), command, probe, and mount paths (they
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
// applies, so existing objects do not change shape.
func templateAnnotations(options Options, valuesHash string) map[string]string {
	if options.RestartedAt == "" && valuesHash == "" {
		return nil
	}
	annotations := map[string]string{}
	if options.RestartedAt != "" {
		annotations[AnnotationRestartedAt] = options.RestartedAt
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
func renderInterceptSlice(serviceName, namespace string, labels map[string]string,
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
	sliceLabels["endpointslice.kubernetes.io/managed-by"] = "skali.dev"
	ready := true
	return &discoveryv1.EndpointSlice{
		TypeMeta: metav1.TypeMeta{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      objectName(serviceName, "local"),
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
