// Package kubernetes deterministically renders a compiled Skali definition
// into Kubernetes API objects. Rendering is pure; applying and observing these
// objects belongs to the later reconciliation layer.
package kubernetes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/layout"
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

	// ManagedCluster enables capability placement on Skali-labeled nodes.
	ManagedCluster bool
}

func Render(result *compiler.Result, options Options) ([]runtime.Object, error) {
	if options.Namespace == "" {
		return nil, fmt.Errorf("kubernetes namespace is required")
	}
	if options.EnvironmentSecretName == "" {
		options.EnvironmentSecretName = EnvironmentSecretName
	}
	if err := compiler.ValidateEnvironment(result, options.Variables); err != nil {
		return nil, err
	}
	var objects []runtime.Object
	for _, key := range sortedKeys(result.Definition.Applications) {
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
	labels := cloneMap(selectorLabels)
	labels[LabelService] = key
	if options.EnvironmentID != "" {
		labels[LabelEnvironment] = options.EnvironmentID
	}
	if options.RevisionChecksum != "" {
		labels[LabelRevision] = RevisionLabelValue(options.RevisionChecksum)
	}
	image := application.Source.Image
	if application.Source.Kind == "build" {
		image = options.BuildImages[key]
		if image == "" {
			return nil, fmt.Errorf("build source has no prepared image")
		}
	}

	container := corev1.Container{
		Name:            key,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args:            append([]string(nil), application.Command...),
		Env:             renderEnvironment(application.Environment, options.EnvironmentSecretName),
		Resources:       renderResources(application.Resources),
	}
	for _, portKey := range sortedKeys(application.Ports) {
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
	for _, volumeKey := range sortedKeys(application.Volumes) {
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
				Labels:    cloneMap(labels),
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceStorage: *resource.NewQuantity(volume.SizeBytes, resource.DecimalSI),
				}},
			},
		})
	}

	autoscalingEnabled := application.Scaling.MaxReplicas > application.Scaling.MinReplicas
	var replicas *int32
	if !autoscalingEnabled {
		replicas = int32Pointer(int32(application.Scaling.MinReplicas))
	}
	graceSeconds := int64(time.Duration(application.Shutdown.GracePeriodMillis) * time.Millisecond / time.Second)
	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: options.Namespace,
			Labels:    cloneMap(labels),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: replicas,
			Selector: &metav1.LabelSelector{MatchLabels: cloneMap(selectorLabels)},
			Strategy: renderStrategy(application.Deployment.Rollout),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: cloneMap(labels)},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: &graceSeconds,
					Containers:                    []corev1.Container{container},
				},
			},
		},
	}
	if options.ManagedCluster {
		deployment.Spec.Template.Spec.NodeSelector = map[string]string{
			layout.CapabilityLabel(layout.CapabilityApplication): layout.CapabilityLabelValue,
		}
	}
	for _, volumeKey := range sortedKeys(application.Volumes) {
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

	servicePorts := renderServicePorts(application)
	if len(servicePorts) > 0 {
		objects = append(objects, &corev1.Service{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: options.Namespace,
				Labels:    cloneMap(labels),
			},
			Spec: corev1.ServiceSpec{
				Selector: cloneMap(selectorLabels),
				Ports:    servicePorts,
			},
		})
	}

	// Every rendered route uses the managed k3s edge.
	ingressClass := "traefik"
	for _, routeKey := range sortedKeys(application.Routes) {
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
				Labels:    cloneMap(labels),
			},
			Spec: networkingv1.IngressSpec{
				IngressClassName: stringPointer(ingressClass),
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
				Labels:    cloneMap(labels),
			},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       name,
				},
				MinReplicas: int32Pointer(int32(application.Scaling.MinReplicas)),
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

func renderEnvironment(environment map[string]compiler.Expression, environmentSecret string) []corev1.EnvVar {
	result := make([]corev1.EnvVar, 0, len(environment))
	for _, name := range sortedKeys(environment) {
		expression := environment[name]
		part := expression.Parts[0]
		variable := corev1.EnvVar{Name: name}
		switch part.Kind {
		case "literal":
			variable.Value = part.Value
		case "project_variable":
			variable.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: environmentSecret},
				Key:                  part.Name,
			}}
		case "service_output":
			variable.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: objectName("skali-output", part.Collection, part.Service)},
				Key:                  part.Output,
			}}
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
	for _, name := range sortedKeys(application.Ports) {
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
	for _, name := range sortedKeys(application.Routes) {
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
			MaxUnavailable: intOrStringPointer(intstr.FromInt32(int32(rollout.MaxUnavailable))),
			MaxSurge:       intOrStringPointer(intstr.FromInt32(int32(rollout.MaxSurge))),
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
		LabelSelector:     &metav1.LabelSelector{MatchLabels: cloneMap(labels)},
	}
	// Kubernetes only permits minDomains with DoNotSchedule. Preferred spread
	// still balances across every available topology domain, while Skali keeps
	// the requested minimum in its IR for health/diagnostic projections.
	if placement.Minimum > 0 && action == corev1.DoNotSchedule {
		constraint.MinDomains = int32Pointer(int32(placement.Minimum))
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

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func stringPointer(value string) *string                              { return &value }
func int32Pointer(value int32) *int32                                 { return &value }
func intOrStringPointer(value intstr.IntOrString) *intstr.IntOrString { return &value }
