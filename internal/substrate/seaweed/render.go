package seaweed

import (
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/platform"
)

// FilerStoreSecret carries the filer's WEED_POSTGRES2_* store config,
// derived from the metadata system claim's outputs. Env only lands at
// container start and a Secret change alone restarts nothing, so the pod
// templates carry a hash of the Secret's data (StoreSpec.StoreConfigHash,
// AnnotationConfigHash): a changed store config rolls the filers.
const FilerStoreSecret = "seaweed-filer-store"

// AnnotationConfigHash is the pod-template annotation carrying the hash of
// the configuration the process reads only at start.
const AnnotationConfigHash = "skali.dev/config-hash"

// VolumeHostPath is where a volume server keeps that node's objects: a
// volume server IS the node's disk (owned-hosts doctrine), so membership is
// the capability label and nothing else.
const VolumeHostPath = "/var/lib/skali/objects"

// SystemLabel marks every seaweed component. Deliberately NOT the managed
// label: environment applies prune managed workload kinds, and platform
// components must never enter that path.
const SystemLabel = "skali.dev/system"

// StoreSpec is the desired shape of the physical system.
type StoreSpec struct {
	Namespace string
	// Masters is 1 or 3 (MastersForNodes).
	Masters int
	// Filers is 1 or 2 (FilersForNodes); zero renders one.
	Filers int
	// Replication is the volume replication code (ReplicationForNodes).
	Replication string
	// Managed pins components to object-storage-capable nodes; local dev
	// renders the all-in-one shape instead.
	Managed bool
	// StoreConfigHash identifies the filer store Secret's content; it is
	// stamped on the filer pod templates so a changed metadata connection
	// rolls the processes that only read it at start. Empty stamps nothing.
	StoreConfigHash string
}

// templateMeta is the pod-template metadata of a component: its labels
// plus the config-hash annotation when the spec carries one.
func templateMeta(spec StoreSpec, labels map[string]string) metav1.ObjectMeta {
	meta := metav1.ObjectMeta{Labels: labels}
	if spec.StoreConfigHash != "" {
		meta.Annotations = map[string]string{AnnotationConfigHash: spec.StoreConfigHash}
	}
	return meta
}

func componentLabels(app string) map[string]string {
	return map[string]string{
		"app":       app,
		SystemLabel: "object-storage",
	}
}

func objectMeta(namespace, name, app string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: namespace,
		Labels:    componentLabels(app),
	}
}

func masterPeers(namespace string, masters int) string {
	peers := make([]string, masters)
	for i := range peers {
		peers[i] = fmt.Sprintf("%s-%d.%s.%s.svc.cluster.local:%d",
			MasterService, i, MasterService, namespace, MasterPort)
	}
	return strings.Join(peers, ",")
}

func ptrIntOrString(value intstr.IntOrString) *intstr.IntOrString { return &value }

func replicas(desired int32) *int32 {
	return &desired
}

// RenderProduction renders the managed multi-component shape: master
// StatefulSet (raft quorum), volume DaemonSet (one server per capable
// node), filer Deployment with the S3 gateway embedded, plus configuration
// and Services. The caller applies everything under FieldManagerPlatform.
func RenderProduction(spec StoreSpec) []runtime.Object {
	objects := []runtime.Object{
		renderMasterConfig(spec.Namespace),
		renderBootstrapS3Config(spec.Namespace),
		renderMasterService(spec.Namespace, MasterService),
		renderMasters(spec),
		renderVolumes(spec),
		renderFiler(spec),
		renderService(spec.Namespace, FilerService, MasterService, FilerService, []corev1.ServicePort{
			{Name: "http", Port: FilerPort},
			{Name: "grpc", Port: FilerGRPCPort},
		}),
		renderService(spec.Namespace, S3Service, MasterService, FilerService, []corev1.ServicePort{
			{Name: "s3", Port: S3Port},
		}),
	}
	if spec.Masters >= 3 {
		objects = append(objects, renderMasterBudget(spec.Namespace))
	}
	if spec.filers() >= 2 {
		objects = append(objects, renderFilerBudget(spec.Namespace))
	}
	return objects
}

// filers is the rendered filer count; an unset spec renders one.
func (spec StoreSpec) filers() int {
	if spec.Filers < 1 {
		return 1
	}
	return spec.Filers
}

// spreadAcrossNodes is the placement rule for a replicated component:
// its pods must spread across nodes (one per node once there are enough),
// required rather than preferred, so replicas never silently share a node
// and a node loss takes at most one of them. A topology spread with a
// skew of one, not anti-affinity: it is satisfiable on a single node (one
// domain has no skew), it lets a rollout surge a second pod onto a node
// while the fleet is full (two against one is a skew of one), and with
// taints honored a failed node stops counting, so its replacement can
// land on a survivor instead of pending. A pod that cannot be placed
// stays pending and shows as a ready count below the recorded shape.
func spreadAcrossNodes(app string) []corev1.TopologySpreadConstraint {
	honor := corev1.NodeInclusionPolicyHonor
	return []corev1.TopologySpreadConstraint{{
		MaxSkew:           1,
		TopologyKey:       "kubernetes.io/hostname",
		WhenUnsatisfiable: corev1.DoNotSchedule,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}},
		NodeTaintsPolicy:  &honor,
	}}
}

// Disruption budgets keep a node drain from taking the store with it: a
// raft quorum tolerates one master away at a time, and one filer must
// keep serving the S3 gateway. A single master or a single filer has no
// budget: a budget of zero tolerated disruptions would only block drains
// forever.
func renderMasterBudget(namespace string) *policyv1.PodDisruptionBudget {
	one := intstr.FromInt32(1)
	return &policyv1.PodDisruptionBudget{
		TypeMeta:   metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"},
		ObjectMeta: objectMeta(namespace, MasterService, MasterService),
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &one,
			Selector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": MasterService}},
		},
	}
}

func renderFilerBudget(namespace string) *policyv1.PodDisruptionBudget {
	one := intstr.FromInt32(1)
	return &policyv1.PodDisruptionBudget{
		TypeMeta:   metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"},
		ObjectMeta: objectMeta(namespace, FilerService, FilerService),
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &one,
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": FilerService}},
		},
	}
}

// componentResources are the requests and limits every store component
// declares, so the scheduler places them honestly and a runaway process
// cannot starve the node. Memory limits stay generous: a volume server
// maps its index files, a filer buffers uploads.
func componentResources(cpu, memory, memoryLimit string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse(memoryLimit),
		},
	}
}

// httpProbe is one HTTP GET probe against a component port.
func httpProbe(path string, port int32, initialDelay, period, failures int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt32(port)},
		},
		InitialDelaySeconds: initialDelay,
		PeriodSeconds:       period,
		FailureThreshold:    failures,
	}
}

// Liveness and startup probes on every component's /healthz (served by
// master, volume server, filer and S3 gateway alike on the pin): a wedged
// process is restarted, and a slow start (a volume server indexing a
// large disk) gets minutes, not seconds, before it counts as failed.
func livenessProbe(port int32) *corev1.Probe { return httpProbe("/healthz", port, 0, 15, 3) }
func startupProbe(port int32) *corev1.Probe  { return httpProbe("/healthz", port, 0, 5, 60) }

// RenderDev renders the all-in-one shape: one `weed server -filer -s3`
// process with a single PVC, plus the same Service names selecting the
// all-in-one pod so endpoints, mirrors, and the probe are mode-independent.
func RenderDev(spec StoreSpec) []runtime.Object {
	return []runtime.Object{
		renderBootstrapS3Config(spec.Namespace),
		renderAllInOnePVC(spec.Namespace),
		renderAllInOne(spec),
		renderService(spec.Namespace, MasterService, MasterService, AllInOneApp, []corev1.ServicePort{
			{Name: "http", Port: MasterPort},
			{Name: "grpc", Port: MasterGRPCPort},
		}),
		renderService(spec.Namespace, FilerService, MasterService, AllInOneApp, []corev1.ServicePort{
			{Name: "http", Port: FilerPort},
			{Name: "grpc", Port: FilerGRPCPort},
		}),
		renderService(spec.Namespace, S3Service, MasterService, AllInOneApp, []corev1.ServicePort{
			{Name: "s3", Port: S3Port},
		}),
	}
}

// renderMasterConfig carries the leader-master maintenance loop:
// self-healing and rebalancing live INSIDE seaweed, not in skalid.
func renderMasterConfig(namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: objectMeta(namespace, "seaweed-master-config", MasterService),
		Data: map[string]string{
			"master.toml": `[master.maintenance]
scripts = """
  lock
  volume.deleteEmpty -quietFor=24h -force
  volume.fix.replication -apply
  volume.balance -force
  unlock
"""
sleep_minutes = 17

[master.volume_growth]
copy_1 = 1
copy_2 = 1
copy_3 = 1
copy_other = 1
`,
		},
	}
}

// renderBootstrapS3Config ships the inert deny identity so authentication
// is on from process start (it merges with the credential store's
// identities at boot, measured on the pin).
func renderBootstrapS3Config(namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: objectMeta(namespace, "seaweed-s3-bootstrap", FilerService),
		Data: map[string]string{
			"s3.json": fmt.Sprintf(`{
  "identities": [
    {
      "name": %q,
      "credentials": [
        {"accessKey": %q, "secretKey": %q}
      ],
      "actions": []
    }
  ]
}
`, BootstrapIdentityName, BootstrapAccessKey, BootstrapSecretKey),
		},
	}
}

// renderMasterService is the raft peer DNS. Not-ready addresses are
// published on purpose: quorum members must resolve each other before any
// of them can become ready.
func renderMasterService(namespace, name string) *corev1.Service {
	return &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: objectMeta(namespace, name, MasterService),
		Spec: corev1.ServiceSpec{
			ClusterIP:                "None",
			PublishNotReadyAddresses: true,
			Selector:                 map[string]string{"app": MasterService},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: MasterPort},
				{Name: "grpc", Port: MasterGRPCPort},
			},
		},
	}
}

func renderService(namespace, name, _ string, selectorApp string, ports []corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: objectMeta(namespace, name, selectorApp),
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": selectorApp},
			Ports:    ports,
		},
	}
}

func capabilitySelector() map[string]string {
	return map[string]string{
		layout.CapabilityLabel(layout.CapabilityObjectStorage): layout.CapabilityLabelValue,
	}
}

// renderMasters is the raft-quorum coordinator StatefulSet: replicas 1 or
// 3, each pinned by its local-path PVC to its first node (a 3-master
// quorum runs on 2 while a node rebuilds, like etcd). The 1 to 3
// transition is a plain re-apply: raft forms and volume ids survive
// (measured on the pin).
func renderMasters(spec StoreSpec) *appsv1.StatefulSet {
	peers := masterPeers(spec.Namespace, spec.Masters)
	// -volumeSizeLimitMB pins volumes small (8 GB, not the 30 GB default):
	// volumes seal sooner, vacuum passes are smaller, rebalancing is
	// finer-grained. New volumes only, which is why it is right from day
	// one.
	args := []string{
		"master",
		"-mdir=/data",
		fmt.Sprintf("-port=%d", MasterPort),
		fmt.Sprintf("-port.grpc=%d", MasterGRPCPort),
		"-ip=$(POD_NAME)." + MasterService + "." + spec.Namespace + ".svc.cluster.local",
		"-peers=" + peers,
		"-volumeSizeLimitMB=8000",
		"-defaultReplication=" + spec.Replication,
	}
	labels := componentLabels(MasterService)
	return &appsv1.StatefulSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: objectMeta(spec.Namespace, MasterService, MasterService),
		Spec: appsv1.StatefulSetSpec{
			ServiceName: MasterService,
			Replicas:    replicas(int32(spec.Masters)),
			Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": MasterService}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					NodeSelector:      capabilitySelector(),
					PriorityClassName: layout.PriorityClassCritical,
					// Quorum members apart from each other: three masters
					// exist only once three capable nodes do, so the rule
					// is satisfiable by construction.
					TopologySpreadConstraints: spreadAcrossNodes(MasterService),
					Containers: []corev1.Container{{
						Name:  "master",
						Image: Image,
						Args:  args,
						Env: []corev1.EnvVar{{
							Name: "POD_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
							},
						}},
						Ports: []corev1.ContainerPort{
							{ContainerPort: MasterPort},
							{ContainerPort: MasterGRPCPort},
						},
						Resources: componentResources("100m", "256Mi", "1Gi"),
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/cluster/status",
									Port: intstr.FromInt32(MasterPort),
								},
							},
							InitialDelaySeconds: 3,
							PeriodSeconds:       10,
						},
						LivenessProbe: livenessProbe(MasterPort),
						StartupProbe:  startupProbe(MasterPort),
						VolumeMounts: []corev1.VolumeMount{
							{Name: "data", MountPath: "/data"},
							{Name: "config", MountPath: "/etc/seaweedfs"},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "seaweed-master-config"},
							},
						},
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			}},
		},
	}
}

// renderVolumes is the volume-server DaemonSet: a volume server IS that
// node's disk (hostPath), so membership is the capability label and
// nothing else. minFreeSpacePercent is load-bearing: seaweed stops
// accepting writes well before it starves the OS, local-path PVCs, and
// images.
func renderVolumes(spec StoreSpec) *appsv1.DaemonSet {
	labels := componentLabels(VolumeApp)
	nodeSelector := capabilitySelector()
	directoryOrCreate := corev1.HostPathDirectoryOrCreate
	return &appsv1.DaemonSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"},
		ObjectMeta: objectMeta(spec.Namespace, VolumeApp, VolumeApp),
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": VolumeApp}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					NodeSelector:      nodeSelector,
					PriorityClassName: layout.PriorityClassCritical,
					Containers: []corev1.Container{{
						Name:  "volume",
						Image: Image,
						Args: []string{
							"volume",
							"-dir=/data",
							"-max=0",
							fmt.Sprintf("-port=%d", VolumePort),
							fmt.Sprintf("-port.grpc=%d", VolumeGRPCPort),
							"-minFreeSpacePercent=10",
							"-mserver=" + masterPeers(spec.Namespace, spec.Masters),
						},
						Ports: []corev1.ContainerPort{
							{ContainerPort: VolumePort},
							{ContainerPort: VolumeGRPCPort},
						},
						Resources: componentResources("100m", "256Mi", "2Gi"),
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/status",
									Port: intstr.FromInt32(VolumePort),
								},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       10,
						},
						LivenessProbe: livenessProbe(VolumePort),
						StartupProbe:  startupProbe(VolumePort),
						VolumeMounts:  []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: VolumeHostPath,
								Type: &directoryOrCreate,
							},
						},
					}},
				},
			},
		},
	}
}

func filerArgs(namespace string, masters int) []string {
	// -s3.config carries the inert bootstrap identity so auth is on from
	// process start (it MERGES with the credential store's identities at
	// boot, measured on the pin); -s3.iam=false keeps the embedded IAM REST
	// API off the tenant-reachable port. Identity administration goes
	// through `weed shell s3.configure` (pod exec), the only supported
	// update channel on the pin.
	return []string{
		"filer",
		"-s3",
		fmt.Sprintf("-port=%d", FilerPort),
		fmt.Sprintf("-port.grpc=%d", FilerGRPCPort),
		fmt.Sprintf("-s3.port=%d", S3Port),
		"-s3.iam=false",
		"-s3.config=/etc/sw/s3.json",
		"-master=" + masterPeers(namespace, masters),
	}
}

// renderFiler is the stateless filer + embedded S3 gateway: stateless
// because the metadata store is the system database claim's tenant,
// injected as WEED_POSTGRES2_* env from the FilerStoreSecret. Until that
// Secret exists the pods sit in CreateContainerConfigError and start on
// their own once it appears: the expected state during first bootstrap,
// not an incident.
func renderFiler(spec StoreSpec) *appsv1.Deployment {
	labels := componentLabels(FilerService)
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: objectMeta(spec.Namespace, FilerService, FilerService),
		Spec: appsv1.DeploymentSpec{
			Replicas: replicas(int32(spec.filers())),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": FilerService}},
			// One filer keeps serving through a rollout: the new pod must be
			// ready before an old one goes.
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: ptrIntOrString(intstr.FromInt32(0)),
					MaxSurge:       ptrIntOrString(intstr.FromInt32(1)),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: templateMeta(spec, labels),
				Spec: corev1.PodSpec{
					PriorityClassName: layout.PriorityClassCritical,
					// Gateways apart from each other: the second filer
					// exists only once a second node does. Filers are
					// stateless and not pinned to capable nodes.
					TopologySpreadConstraints: spreadAcrossNodes(FilerService),
					Containers: []corev1.Container{{
						Name:  "filer",
						Image: Image,
						Args:  filerArgs(spec.Namespace, spec.Masters),
						EnvFrom: []corev1.EnvFromSource{{
							SecretRef: &corev1.SecretEnvSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: FilerStoreSecret},
							},
						}},
						Ports: []corev1.ContainerPort{
							{ContainerPort: FilerPort},
							{ContainerPort: FilerGRPCPort},
							{ContainerPort: S3Port},
						},
						Resources: componentResources("200m", "512Mi", "2Gi"),
						// Readiness is the S3 gateway answering: that is the
						// port the Service routes tenants to.
						ReadinessProbe: httpProbe("/healthz", S3Port, 3, 10, 3),
						LivenessProbe:  livenessProbe(FilerPort),
						StartupProbe:   startupProbe(FilerPort),
						VolumeMounts:   []corev1.VolumeMount{{Name: "s3-bootstrap", MountPath: "/etc/sw"}},
					}},
					Volumes: []corev1.Volume{{
						Name: "s3-bootstrap",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "seaweed-s3-bootstrap"},
							},
						},
					}},
				},
			},
		},
	}
}

// renderAllInOnePVC is the dev store's one volume; Recreate strategy keeps
// pod and volume on the single node (the registry precedent).
func renderAllInOnePVC(namespace string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: objectMeta(namespace, "seaweed-data", AllInOneApp),
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("2Gi"),
				},
			},
		},
	}
}

// renderAllInOne is the dev shape: master, volume, filer, and S3 gateway in
// one `weed server` process with one PVC.
func renderAllInOne(spec StoreSpec) *appsv1.Deployment {
	labels := componentLabels(AllInOneApp)
	args := []string{
		"server",
		"-dir=/data",
		"-master.volumeSizeLimitMB=1000",
		fmt.Sprintf("-master.port=%d", MasterPort),
		fmt.Sprintf("-volume.port=%d", VolumePort),
		"-volume.max=0",
		"-volume.minFreeSpacePercent=5",
		"-filer",
		fmt.Sprintf("-filer.port=%d", FilerPort),
		"-s3",
		fmt.Sprintf("-s3.port=%d", S3Port),
		"-s3.config=/etc/sw/s3.json",
	}
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: objectMeta(spec.Namespace, AllInOneApp, AllInOneApp),
		Spec: appsv1.DeploymentSpec{
			Replicas: replicas(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": AllInOneApp}},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: templateMeta(spec, labels),
				Spec: corev1.PodSpec{
					PriorityClassName: layout.PriorityClassCritical,
					Containers: []corev1.Container{{
						Name:  "seaweed",
						Image: Image,
						Args:  args,
						EnvFrom: []corev1.EnvFromSource{{
							SecretRef: &corev1.SecretEnvSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: FilerStoreSecret},
							},
						}},
						Ports: []corev1.ContainerPort{
							{ContainerPort: MasterPort},
							{ContainerPort: FilerPort},
							{ContainerPort: S3Port},
						},
						Resources:      componentResources("100m", "256Mi", "2Gi"),
						ReadinessProbe: httpProbe("/healthz", S3Port, 3, 5, 3),
						LivenessProbe:  livenessProbe(FilerPort),
						StartupProbe:   startupProbe(FilerPort),
						VolumeMounts: []corev1.VolumeMount{
							{Name: "data", MountPath: "/data"},
							{Name: "s3-bootstrap", MountPath: "/etc/sw"},
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: "seaweed-data",
								},
							},
						},
						{
							Name: "s3-bootstrap",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "seaweed-s3-bootstrap"},
								},
							},
						},
					},
				},
			},
		},
	}
}

// RenderFilerStoreSecret derives the filer's postgres2 store config from
// the metadata claim's outputs. createTable is what makes bucket deletion a
// DROP TABLE (table-per-bucket). The image ships a default filer.toml with
// leveldb2 enabled; two enabled stores are a fatal config error, so
// leveldb2 is explicitly off.
func RenderFilerStoreSecret(namespace, host string, port int, username, password, database string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: objectMeta(namespace, FilerStoreSecret, FilerService),
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"WEED_LEVELDB2_ENABLED":   "false",
			"WEED_POSTGRES2_ENABLED":  "true",
			"WEED_POSTGRES2_HOSTNAME": host,
			"WEED_POSTGRES2_PORT":     fmt.Sprint(port),
			"WEED_POSTGRES2_USERNAME": username,
			"WEED_POSTGRES2_PASSWORD": password,
			"WEED_POSTGRES2_DATABASE": database,
			"WEED_POSTGRES2_SSLMODE":  "disable",
			"WEED_POSTGRES2_CREATETABLE": `CREATE TABLE IF NOT EXISTS "%s" (` +
				`dirhash BIGINT, name VARCHAR(65535), directory VARCHAR(65535), meta bytea, ` +
				`PRIMARY KEY (dirhash, name))`,
		},
	}
}

// RenderFence is the network fence (a security invariant, not tuning):
// seaweed's filer/master/volume HTTP APIs are unauthenticated by design and
// tenant workloads share the cluster network. Every seaweed pod
// default-denies ingress except from other seaweed pods; the S3 port
// (RenderS3AccessPolicy) and skalid's admin path (RenderAccessPolicy) are
// opened separately. Policies union.
func RenderFence(namespace string) []runtime.Object {
	seaweedPods := metav1.LabelSelector{
		MatchLabels: map[string]string{SystemLabel: "object-storage"},
	}
	internal := &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: objectMeta(namespace, "seaweed-internal", MasterService),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: seaweedPods,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{PodSelector: &seaweedPods}},
			}},
		},
	}
	return []runtime.Object{internal}
}

// S3AccessPolicy names the policy that opens the S3 port to claim holders.
const S3AccessPolicy = "seaweed-s3-access"

// LegacyS3OpenPolicy named the policy that opened the S3 port to every pod
// in the cluster before claim holders were admitted by name. Nothing
// renders it; the substrate deletes it once per process so an upgraded
// installation does not keep the port open beside the new policy (policies
// union). Retain while upgrades from releases carrying it are supported.
const LegacyS3OpenPolicy = "seaweed-s3-open"

// RenderS3AccessPolicy admits the S3 port to exactly the namespaces holding
// a bucket claim plus the platform's fixed peers: the skalid pod (the
// platform S3 client, backups) and the edge (bucket routes proxy to the
// gateway). The readiness probe rides the service proxy (peers.ProxyCIDRs),
// and a local platform's host processes the loopback NodePort
// (peers.HostExcept). The fixed peers keep the first rule non-empty; every
// other rule is rendered only with peers, because a rule without any
// admits everyone.
func RenderS3AccessPolicy(namespace string, peers platform.AccessPeers) *networkingv1.NetworkPolicy {
	s3Port := intstr.FromInt32(S3Port)
	tcp := corev1.ProtocolTCP
	ports := []networkingv1.NetworkPolicyPort{{Port: &s3Port, Protocol: &tcp}}
	rules := []networkingv1.NetworkPolicyIngressRule{{
		From: []networkingv1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": bundle.Namespace}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "skalid"}},
			},
			{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": platform.EdgeNamespace}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{platform.EdgePodLabel: platform.EdgePodName}},
			},
		},
		Ports: ports,
	}}
	if from := environmentPeers(peers); len(from) > 0 {
		rules = append(rules, networkingv1.NetworkPolicyIngressRule{From: from, Ports: ports})
	}
	if from := cidrPeers(peers.ProxyCIDRs); len(from) > 0 {
		rules = append(rules, networkingv1.NetworkPolicyIngressRule{From: from, Ports: ports})
	}
	if peers.HostExcept != nil {
		rules = append(rules, networkingv1.NetworkPolicyIngressRule{From: hostPeers(peers.HostExcept), Ports: ports})
	}
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: objectMeta(namespace, S3AccessPolicy, MasterService),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{SystemLabel: "object-storage"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     rules,
		},
	}
}

// environmentPeers selects each holder's namespace by its environment
// label, in a stable order.
func environmentPeers(peers platform.AccessPeers) []networkingv1.NetworkPolicyPeer {
	var from []networkingv1.NetworkPolicyPeer
	for _, id := range peers.EnvironmentIDs() {
		from = append(from, networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{platform.EnvironmentLabel: id},
		}})
	}
	return from
}

func cidrPeers(cidrs []string) []networkingv1.NetworkPolicyPeer {
	var from []networkingv1.NetworkPolicyPeer
	for _, cidr := range cidrs {
		from = append(from, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr}})
	}
	return from
}

// hostPeers admits every source outside the pod CIDRs: on a local platform
// that is the host behind the loopback NodePorts (and the node itself).
func hostPeers(podCIDRs []string) []networkingv1.NetworkPolicyPeer {
	return []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{
		CIDR:   "0.0.0.0/0",
		Except: slices.Clone(podCIDRs),
	}}}
}

// RenderAccessPolicy admits skalid's traffic (via the API server's service
// proxy) to the fenced filer and master ports. cidrs comes from
// kube.NodeProxyCIDRs: per-control-plane-node /32s, never a whole pod
// CIDR, which would include every tenant pod and void the fence.
func RenderAccessPolicy(namespace string, cidrs []string) *networkingv1.NetworkPolicy {
	from := make([]networkingv1.NetworkPolicyPeer, len(cidrs))
	for i, cidr := range cidrs {
		from[i] = networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}
	}
	filerPort := intstr.FromInt32(FilerPort)
	masterPort := intstr.FromInt32(MasterPort)
	tcp := corev1.ProtocolTCP
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: objectMeta(namespace, "seaweed-skalid-access", MasterService),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{SystemLabel: "object-storage"},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: from,
				Ports: []networkingv1.NetworkPolicyPort{
					{Port: &filerPort, Protocol: &tcp},
					{Port: &masterPort, Protocol: &tcp},
				},
			}},
		},
	}
}

// RenderDevS3NodePort exposes the dev all-in-one S3 gateway on a fixed
// NodePort for the local platform's loopback port maps (skali dev maps the
// identical number on 127.0.0.1). Dev shape only; managed clusters publish
// S3 through the ingress instead.
func RenderDevS3NodePort(namespace string) *corev1.Service {
	return &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: objectMeta(namespace, S3ExternalService, AllInOneApp),
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: map[string]string{"app": AllInOneApp},
			Ports: []corev1.ServicePort{{
				Name:     "s3",
				Port:     S3Port,
				NodePort: bundle.S3NodePort,
			}},
		},
	}
}
