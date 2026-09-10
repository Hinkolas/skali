package observe

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/module"
)

// SourceOptions tunes the Kubernetes watch source.
type SourceOptions struct {
	// Resync re-fires update handlers for every cached object: the
	// correctness backstop against missed watch edits.
	Resync time.Duration
	// StaleThreshold is how long after a list/watch failure without a
	// successful re-establishment the source reports stale.
	StaleThreshold time.Duration
	// Enqueue receives the affected environment of every cache change; nil
	// disables enqueueing (observation-only tests).
	Enqueue func(uuid.UUID)
	// Dynamic adds label-selected dynamic informers for blessed operator
	// CRDs (CNPG). The CRDs must exist on the cluster: skali-managed
	// installations always install the operators before skalid, and a
	// missing CRD keeps the source from ever reporting fresh.
	Dynamic []DynamicKind
}

// DynamicKind is one dynamically watched CRD with its projection.
type DynamicKind struct {
	Kind    string
	GVR     schema.GroupVersionResource
	Convert func(*unstructured.Unstructured) (Object, bool)
}

// KubeSource feeds the observed store from Kubernetes LIST/WATCH caches:
// initial LIST per kind, wait for cache sync, WATCH from the returned
// resource versions, convert every change into a store write plus an
// affected-owner enqueue. Watched kinds are the core set plus the
// dynamic operator CRDs registered through SourceOptions.Dynamic (CNPG).
type KubeSource struct {
	client    *kube.Client
	store     *Store
	opts      SourceOptions
	informers []namedInformer

	mu          sync.Mutex
	current     map[string]watch.Interface // kind -> live watch connection
	lastRefresh time.Time
}

type namedInformer struct {
	kind     string
	informer cache.SharedIndexInformer
}

// KindSync reports one watched kind's cache synchronization state, for the
// structural observation diagnostics endpoint.
type KindSync struct {
	Kind   string
	Synced bool
}

// SyncStates lists every watched kind with its cache sync state.
func (k *KubeSource) SyncStates() []KindSync {
	states := make([]KindSync, 0, len(k.informers))
	for _, entry := range k.informers {
		states = append(states, KindSync{Kind: entry.kind, Synced: entry.informer.HasSynced()})
	}
	return states
}

func NewKubeSource(client *kube.Client, store *Store, opts SourceOptions) *KubeSource {
	if opts.Resync <= 0 {
		opts.Resync = 5 * time.Minute
	}
	if opts.StaleThreshold <= 0 {
		opts.StaleThreshold = 30 * time.Second
	}
	k := &KubeSource{client: client, store: store, opts: opts}
	k.register()
	return k
}

// Run starts every informer, waits for the initial cache synchronization,
// flips the store fresh, and then keeps evaluating freshness until the
// context ends. Health is unknown until the sync completes by contract.
func (k *KubeSource) Run(ctx context.Context) error {
	defer k.store.MarkUnready(SourceKubernetes)
	synced := make([]cache.InformerSynced, 0, len(k.informers))
	for _, entry := range k.informers {
		go entry.informer.RunWithContext(ctx)
		synced = append(synced, entry.informer.HasSynced)
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced...) {
		return ctx.Err()
	}
	k.store.MarkReady(SourceKubernetes)

	tick := max(time.Second, min(10*time.Second, k.opts.StaleThreshold/3))
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			k.store.EvaluateFreshness(SourceKubernetes, k.opts.StaleThreshold)
		}
	}
}

func (k *KubeSource) register() {
	core := k.client.Clientset.CoreV1()
	apps := k.client.Clientset.AppsV1()
	batch := k.client.Clientset.BatchV1()
	networking := k.client.Clientset.NetworkingV1()
	autoscaling := k.client.Clientset.AutoscalingV2()
	all := metav1.NamespaceAll
	managed := rendering.ManagedSelector

	k.addObjectInformer("Pod", &corev1.Pod{}, k.listWatch("Pod",
		func(o metav1.ListOptions) (runtime.Object, error) {
			return core.Pods(all).List(context.Background(), o)
		},
		func(o metav1.ListOptions) (watch.Interface, error) {
			return core.Pods(all).Watch(context.Background(), o)
		},
		managed, ""), convertPod)

	k.addObjectInformer("Deployment", &appsv1.Deployment{}, k.listWatch("Deployment",
		func(o metav1.ListOptions) (runtime.Object, error) {
			return apps.Deployments(all).List(context.Background(), o)
		},
		func(o metav1.ListOptions) (watch.Interface, error) {
			return apps.Deployments(all).Watch(context.Background(), o)
		},
		managed, ""), convertDeployment)

	// Release-command Jobs: the reconciler's release gate reads their
	// terminal state, and a completing Job must poke its environment instead
	// of waiting out the requeue interval.
	k.addObjectInformer("Job", &batchv1.Job{}, k.listWatch("Job",
		func(o metav1.ListOptions) (runtime.Object, error) {
			return batch.Jobs(all).List(context.Background(), o)
		},
		func(o metav1.ListOptions) (watch.Interface, error) {
			return batch.Jobs(all).Watch(context.Background(), o)
		},
		managed, ""), convertJob)

	k.addObjectInformer("Service", &corev1.Service{}, k.listWatch("Service",
		func(o metav1.ListOptions) (runtime.Object, error) {
			return core.Services(all).List(context.Background(), o)
		},
		func(o metav1.ListOptions) (watch.Interface, error) {
			return core.Services(all).Watch(context.Background(), o)
		},
		managed, ""), convertService)

	k.addObjectInformer("Ingress", &networkingv1.Ingress{}, k.listWatch("Ingress",
		func(o metav1.ListOptions) (runtime.Object, error) {
			return networking.Ingresses(all).List(context.Background(), o)
		},
		func(o metav1.ListOptions) (watch.Interface, error) {
			return networking.Ingresses(all).Watch(context.Background(), o)
		},
		managed, ""), convertPlain(schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"}, module.KindIngress))

	// Intercept EndpointSlices (local dev). The selector names skali's own
	// managed-by value on top of the managed label: the endpointslice
	// controller copies every Service label onto the slices it manages, so
	// the managed label alone would expose every ordinary Service's slice
	// as an undesired managed object and the kernel would prune it on every
	// pass (the controller recreating it each time).
	k.addObjectInformer("EndpointSlice", &discoveryv1.EndpointSlice{}, k.listWatch("EndpointSlice",
		func(o metav1.ListOptions) (runtime.Object, error) {
			return k.client.Clientset.DiscoveryV1().EndpointSlices(all).List(context.Background(), o)
		},
		func(o metav1.ListOptions) (watch.Interface, error) {
			return k.client.Clientset.DiscoveryV1().EndpointSlices(all).Watch(context.Background(), o)
		},
		rendering.InterceptEndpointSliceSelector, ""), convertPlain(schema.GroupVersionKind{Group: "discovery.k8s.io", Version: "v1", Kind: "EndpointSlice"}, module.KindEndpointSlice))

	k.addObjectInformer("PersistentVolumeClaim", &corev1.PersistentVolumeClaim{}, k.listWatch("PersistentVolumeClaim",
		func(o metav1.ListOptions) (runtime.Object, error) {
			return core.PersistentVolumeClaims(all).List(context.Background(), o)
		},
		func(o metav1.ListOptions) (watch.Interface, error) {
			return core.PersistentVolumeClaims(all).Watch(context.Background(), o)
		},
		managed, ""), convertPlain(schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaim"}, module.KindVolume))

	k.addObjectInformer("HorizontalPodAutoscaler", &autoscalingv2.HorizontalPodAutoscaler{}, k.listWatch("HorizontalPodAutoscaler",
		func(o metav1.ListOptions) (runtime.Object, error) {
			return autoscaling.HorizontalPodAutoscalers(all).List(context.Background(), o)
		},
		func(o metav1.ListOptions) (watch.Interface, error) {
			return autoscaling.HorizontalPodAutoscalers(all).Watch(context.Background(), o)
		},
		managed, ""), convertAutoscaler)

	k.addObjectInformer("Namespace", &corev1.Namespace{}, k.listWatch("Namespace",
		func(o metav1.ListOptions) (runtime.Object, error) {
			return core.Namespaces().List(context.Background(), o)
		},
		func(o metav1.ListOptions) (watch.Interface, error) {
			return core.Namespaces().Watch(context.Background(), o)
		},
		managed, ""), convertNamespace)

	// Nodes are unlabeled infrastructure: observed only to fan a node change
	// out to the environments with pods placed on it.
	k.addNodeInformer(k.listWatch("Node",
		func(o metav1.ListOptions) (runtime.Object, error) { return core.Nodes().List(context.Background(), o) },
		func(o metav1.ListOptions) (watch.Interface, error) {
			return core.Nodes().Watch(context.Background(), o)
		},
		"", ""))

	// Warning events join to environments through their involved object.
	k.addEventInformer(k.listWatch("Event",
		func(o metav1.ListOptions) (runtime.Object, error) {
			return core.Events(all).List(context.Background(), o)
		},
		func(o metav1.ListOptions) (watch.Interface, error) {
			return core.Events(all).Watch(context.Background(), o)
		},
		"", "type=Warning"))

	// Blessed operator CRDs through the dynamic client, same selector, same
	// store contract.
	for _, dynamicKind := range k.opts.Dynamic {
		gvr := dynamicKind.GVR
		convert := dynamicKind.Convert
		k.addObjectInformer(dynamicKind.Kind, &unstructured.Unstructured{}, k.listWatch(dynamicKind.Kind,
			func(o metav1.ListOptions) (runtime.Object, error) {
				return k.client.Dynamic.Resource(gvr).Namespace(all).List(context.Background(), o)
			},
			func(o metav1.ListOptions) (watch.Interface, error) {
				return k.client.Dynamic.Resource(gvr).Namespace(all).Watch(context.Background(), o)
			},
			managed, ""), func(raw any) (Object, bool) {
			object, ok := raw.(*unstructured.Unstructured)
			if !ok {
				return Object{}, false
			}
			return convert(object)
		})
	}
}

// listWatch builds a contact-tracking ListerWatcher. A successful LIST is
// real contact; a Watch call returning without error is NOT, because the
// rest client masks probable-EOF connection errors as an empty watcher so
// reflectors retry silently. Contact is therefore marked on the first
// delivered watch event (initial or bookmark events count), and an
// immediately closing zero-event watcher is recorded as a failure.
func (k *KubeSource) listWatch(
	kind string,
	list func(metav1.ListOptions) (runtime.Object, error),
	watchFn func(metav1.ListOptions) (watch.Interface, error),
	labelSelector, fieldSelector string,
) cache.ListerWatcher {
	return &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.LabelSelector = labelSelector
			options.FieldSelector = fieldSelector
			result, err := list(options)
			if err != nil {
				k.store.MarkFailure(SourceKubernetes)
				return nil, err
			}
			k.store.MarkContact(SourceKubernetes)
			return result, nil
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.LabelSelector = labelSelector
			options.FieldSelector = fieldSelector
			result, err := watchFn(options)
			if err != nil {
				k.store.MarkFailure(SourceKubernetes)
				return nil, err
			}
			return k.trackContact(kind, result), nil
		},
	}
}

// emptyWatchWindow separates a masked connection failure (an empty watcher
// that closes immediately) from a healthy server-side watch timeout, which
// closes after minutes and virtually always after a bookmark.
const emptyWatchWindow = 2 * time.Second

// trackContact forwards a watcher's events, marking contact on the first
// delivered NON-ERROR event and a failure when the watch closes without one
// within the empty-watch window. Error events are excluded deliberately: a
// booting or degraded API server answers watch requests with error statuses
// long before it can serve a fresh view, and treating those as contact
// would keep resetting the staleness clock through a real outage.
//
// Each connection also registers as its kind's current watcher, the handle
// Refresh uses to force a re-establishment on demand.
func (k *KubeSource) trackContact(kind string, inner watch.Interface) watch.Interface {
	k.setCurrent(kind, inner)
	forwarder := &contactWatcher{inner: inner, out: make(chan watch.Event)}
	go func() {
		started := time.Now()
		delivered := false
		for event := range inner.ResultChan() {
			if !delivered && event.Type != watch.Error {
				delivered = true
				k.store.MarkContact(SourceKubernetes)
			}
			forwarder.out <- event
		}
		if !delivered && time.Since(started) < emptyWatchWindow {
			k.store.MarkFailure(SourceKubernetes)
		}
		k.clearCurrent(kind, inner)
		close(forwarder.out)
	}()
	return forwarder
}

// refreshMinInterval rate-limits Refresh: the kernel asks on every stalled
// health pass, the reflectors need only one bounce to re-list.
const refreshMinInterval = 30 * time.Second

// Refresh forces every kind's current watch connection to re-establish.
// This is the level-triggered recovery for observation gaps no event will
// ever heal: an object created while the informers were still establishing
// can be missing from a perfectly live watch (the initial list served a
// stale view and the creation event was never replayed), and it stays
// invisible until something touches it. Stopping the current connections
// makes every reflector re-list or replay from its last resource version,
// restoring the missing objects within seconds. Rate-limited internally;
// repeated and concurrent calls are cheap no-ops.
func (k *KubeSource) Refresh() {
	k.mu.Lock()
	if time.Since(k.lastRefresh) < refreshMinInterval {
		k.mu.Unlock()
		return
	}
	k.lastRefresh = time.Now()
	watchers := make([]watch.Interface, 0, len(k.current))
	for _, watcher := range k.current {
		watchers = append(watchers, watcher)
	}
	k.mu.Unlock()
	slog.Info("observe: refreshing watch connections", "connections", len(watchers))
	for _, watcher := range watchers {
		watcher.Stop()
	}
}

func (k *KubeSource) setCurrent(kind string, watcher watch.Interface) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.current == nil {
		k.current = make(map[string]watch.Interface)
	}
	k.current[kind] = watcher
}

func (k *KubeSource) clearCurrent(kind string, watcher watch.Interface) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.current[kind] == watcher {
		delete(k.current, kind)
	}
}

type contactWatcher struct {
	inner watch.Interface
	out   chan watch.Event
}

func (c *contactWatcher) Stop()                          { c.inner.Stop() }
func (c *contactWatcher) ResultChan() <-chan watch.Event { return c.out }

// addObjectInformer wires one watched kind into the store: every add or
// update upserts the converted projection, every delete removes it, and
// each change enqueues the affected environment.
func (k *KubeSource) addObjectInformer(kind string, example runtime.Object, lw cache.ListerWatcher, convert func(any) (Object, bool)) {
	informer := cache.NewSharedIndexInformer(lw, example, k.opts.Resync, cache.Indexers{})
	k.watchErrors(informer)
	_, _ = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(raw any) {
			if obj, ok := convert(raw); ok {
				k.store.Upsert(obj)
				k.enqueueAffected(obj)
			}
		},
		UpdateFunc: func(_, raw any) {
			if obj, ok := convert(raw); ok {
				k.store.Upsert(obj)
				k.enqueueAffected(obj)
			}
		},
		DeleteFunc: func(raw any) {
			if tombstone, ok := raw.(cache.DeletedFinalStateUnknown); ok {
				raw = tombstone.Obj
			}
			if obj, ok := convert(raw); ok {
				k.store.Remove(obj.Ref)
				k.enqueueAffected(obj)
			}
		},
	})
	k.informers = append(k.informers, namedInformer{kind: kind, informer: informer})
}

// enqueueAffected pokes the object's environment; a platform-scoped shared
// object (a database pool) fans out to every environment referencing it.
func (k *KubeSource) enqueueAffected(obj Object) {
	if obj.Environment != uuid.Nil || obj.SharedKey == "" {
		k.enqueue(obj.Environment)
		return
	}
	for _, environment := range k.store.EnvironmentsForSharedKey(obj.SharedKey) {
		k.enqueue(environment)
	}
}

func (k *KubeSource) addNodeInformer(lw cache.ListerWatcher) {
	informer := cache.NewSharedIndexInformer(lw, &corev1.Node{}, k.opts.Resync, cache.Indexers{})
	k.watchErrors(informer)
	asNode := func(raw any) (*corev1.Node, bool) {
		if tombstone, ok := raw.(cache.DeletedFinalStateUnknown); ok {
			raw = tombstone.Obj
		}
		node, ok := raw.(*corev1.Node)
		return node, ok
	}
	fanOut := func(node *corev1.Node) {
		for _, environment := range k.store.EnvironmentsOnNode(node.Name) {
			k.enqueue(environment)
		}
	}
	upsert := func(raw any) {
		if node, ok := asNode(raw); ok {
			k.store.SetNodeArch(node.Name, nodeArch(node))
			k.store.SetNodeCapabilities(node.Name, nodeCapabilities(node))
			k.store.SetNodeRecord(nodeRecord(node))
			fanOut(node)
		}
	}
	_, _ = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    upsert,
		UpdateFunc: func(_, raw any) { upsert(raw) },
		DeleteFunc: func(raw any) {
			if node, ok := asNode(raw); ok {
				k.store.RemoveNode(node.Name)
				fanOut(node)
			}
		},
	})
	k.informers = append(k.informers, namedInformer{kind: "Node", informer: informer})
}

// nodeArch reads a node's CPU architecture, preferring the kubelet-reported
// value over the standard arch label.
func nodeArch(node *corev1.Node) string {
	if arch := node.Status.NodeInfo.Architecture; arch != "" {
		return arch
	}
	return node.Labels["kubernetes.io/arch"]
}

// nodeCapabilities reads the installer-stamped capability labels.
func nodeCapabilities(node *corev1.Node) []string {
	var capabilities []string
	for label, value := range node.Labels {
		if value != layout.CapabilityLabelValue {
			continue
		}
		if capability, ok := strings.CutPrefix(label, layout.CapabilityLabelPrefix); ok {
			capabilities = append(capabilities, capability)
		}
	}
	return capabilities
}

// nodeRecord projects the member-visible node facts out of the full object.
func nodeRecord(node *corev1.Node) NodeRecord {
	record := NodeRecord{
		Name:           node.Name,
		Role:           layout.RoleFromLabels(node.Labels),
		Capabilities:   nodeCapabilities(node),
		Arch:           nodeArch(node),
		OS:             node.Status.NodeInfo.OSImage,
		KubeletVersion: node.Status.NodeInfo.KubeletVersion,
		Schedulable:    !node.Spec.Unschedulable,
	}
	sort.Strings(record.Capabilities)
	for _, condition := range node.Status.Conditions {
		if condition.Type != corev1.NodeReady {
			continue
		}
		record.Ready = condition.Status == corev1.ConditionTrue
		record.LastHeartbeat = condition.LastHeartbeatTime.Time
	}
	for _, address := range node.Status.Addresses {
		switch address.Type {
		case corev1.NodeInternalIP:
			if record.InternalIP == "" {
				record.InternalIP = address.Address
			}
		case corev1.NodeExternalIP:
			if record.ExternalIP == "" {
				record.ExternalIP = address.Address
			}
		}
	}
	return record
}

func (k *KubeSource) addEventInformer(lw cache.ListerWatcher) {
	informer := cache.NewSharedIndexInformer(lw, &corev1.Event{}, k.opts.Resync, cache.Indexers{})
	k.watchErrors(informer)
	record := func(raw any) {
		event, ok := raw.(*corev1.Event)
		if !ok {
			return
		}
		gvk := schema.FromAPIVersionAndKind(event.InvolvedObject.APIVersion, event.InvolvedObject.Kind)
		ref := kube.ObjectRef{GVK: gvk, Namespace: event.InvolvedObject.Namespace, Name: event.InvolvedObject.Name}
		environment, owned := k.store.Environment(ref)
		if !owned {
			return
		}
		k.store.RecordEvent(ref, EventRecord{
			Reason:  event.Reason,
			Message: event.Message,
			Count:   event.Count,
			At:      eventTime(event),
		})
		k.enqueue(environment)
	}
	_, _ = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    record,
		UpdateFunc: func(_, raw any) { record(raw) },
	})
	k.informers = append(k.informers, namedInformer{kind: "Event", informer: informer})
}

func (k *KubeSource) watchErrors(informer cache.SharedIndexInformer) {
	_ = informer.SetWatchErrorHandler(func(_ *cache.Reflector, _ error) {
		k.store.MarkFailure(SourceKubernetes)
	})
}

func (k *KubeSource) enqueue(environmentID uuid.UUID) {
	if k.opts.Enqueue != nil && environmentID != uuid.Nil {
		k.opts.Enqueue(environmentID)
	}
}

func eventTime(event *corev1.Event) time.Time {
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	return event.CreationTimestamp.Time
}

// identity extracts the shared label-derived identity of a managed object.
func identity(meta metav1.Object) (environment uuid.UUID, service, revision string) {
	labels := meta.GetLabels()
	if parsed, err := uuid.Parse(labels[rendering.LabelEnvironment]); err == nil {
		environment = parsed
	}
	service = labels[rendering.LabelService]
	if service == "" {
		service = labels[rendering.LabelApplication]
	}
	revision = labels[rendering.LabelRevision]
	return environment, service, revision
}

func convertDeployment(raw any) (Object, bool) {
	deployment, ok := raw.(*appsv1.Deployment)
	if !ok {
		return Object{}, false
	}
	environment, service, revision := identity(deployment)
	desired := int32(-1) // autoscaler-owned when absent from the spec
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	conditions := make([]module.Condition, 0, len(deployment.Status.Conditions))
	for _, condition := range deployment.Status.Conditions {
		conditions = append(conditions, module.Condition{
			Type:    string(condition.Type),
			Status:  string(condition.Status),
			Reason:  condition.Reason,
			Message: condition.Message,
		})
	}
	return Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			Namespace: deployment.Namespace, Name: deployment.Name, UID: deployment.UID,
		},
		Kind: module.KindWorkload, Name: service,
		Labels:      deployment.Labels,
		Environment: environment, Service: service, Revision: revision,
		Color:         deployment.Labels[rendering.LabelColor],
		Generation:    deployment.Generation,
		ManagedFields: deployment.ManagedFields,
		Workload: &module.WorkloadStatus{
			Desired:            desired,
			Ready:              deployment.Status.ReadyReplicas,
			Updated:            deployment.Status.UpdatedReplicas,
			Available:          deployment.Status.AvailableReplicas,
			Generation:         deployment.Generation,
			ObservedGeneration: deployment.Status.ObservedGeneration,
			Conditions:         conditions,
		},
	}, true
}

func convertJob(raw any) (Object, bool) {
	job, ok := raw.(*batchv1.Job)
	if !ok {
		return Object{}, false
	}
	environment, service, revision := identity(job)
	status := &JobStatus{
		Succeeded: job.Status.Succeeded > 0,
		Created:   job.CreationTimestamp.Time,
	}
	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobComplete:
			status.Succeeded = true
		case batchv1.JobFailed:
			status.Failed = true
			status.Reason = condition.Reason
			status.Message = condition.Message
		}
	}
	return Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			Namespace: job.Namespace, Name: job.Name, UID: job.UID,
		},
		Kind: KindReleaseJob, Name: job.Name,
		Labels:      job.Labels,
		Environment: environment, Service: service, Revision: revision,
		Generation: job.Generation,
		Job:        status,
	}, true
}

func convertPod(raw any) (Object, bool) {
	pod, ok := raw.(*corev1.Pod)
	if !ok {
		return Object{}, false
	}
	environment, service, revision := identity(pod)
	status := &module.PodStatus{
		Phase: string(pod.Status.Phase),
		Node:  pod.Spec.NodeName,
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			status.Ready = condition.Status == corev1.ConditionTrue
		}
	}
	for _, container := range pod.Status.ContainerStatuses {
		status.Restarts += container.RestartCount
		if waiting := container.State.Waiting; waiting != nil && status.Reason == "" {
			status.Reason = waiting.Reason
			status.Message = waiting.Message
		}
	}
	if status.Reason == "" {
		status.Reason = pod.Status.Reason
		status.Message = pod.Status.Message
	}
	if pod.Status.StartTime != nil {
		status.Started = pod.Status.StartTime.Time
	}
	return Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
			Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID,
		},
		Kind: module.KindPod, Name: pod.Name,
		Labels:      pod.Labels,
		Environment: environment, Service: service, Revision: revision,
		Color: pod.Labels[rendering.LabelColor],
		Node:  pod.Spec.NodeName,
		Pod:   status,
	}, true
}

func convertAutoscaler(raw any) (Object, bool) {
	autoscaler, ok := raw.(*autoscalingv2.HorizontalPodAutoscaler)
	if !ok {
		return Object{}, false
	}
	environment, service, revision := identity(autoscaler)
	minReplicas := int32(1)
	if autoscaler.Spec.MinReplicas != nil {
		minReplicas = *autoscaler.Spec.MinReplicas
	}
	return Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"},
			Namespace: autoscaler.Namespace, Name: autoscaler.Name, UID: autoscaler.UID,
		},
		Kind: module.KindAutoscaler, Name: service,
		Labels:      autoscaler.Labels,
		Environment: environment, Service: service, Revision: revision,
		Autoscaler: &module.AutoscalerStatus{
			Min:             minReplicas,
			Max:             autoscaler.Spec.MaxReplicas,
			Current:         autoscaler.Status.CurrentReplicas,
			DesiredReplicas: autoscaler.Status.DesiredReplicas,
		},
	}, true
}

func convertNamespace(raw any) (Object, bool) {
	namespace, ok := raw.(*corev1.Namespace)
	if !ok {
		return Object{}, false
	}
	environment, _, _ := identity(namespace)
	return Object{
		Ref: kube.ObjectRef{
			GVK:  schema.GroupVersionKind{Version: "v1", Kind: "Namespace"},
			Name: namespace.Name, UID: namespace.UID,
		},
		Kind: kindNamespace, Name: namespace.Name,
		Labels:      namespace.Labels,
		Environment: environment,
	}, true
}

// convertPlain projects kinds that carry identity but no typed status
// (Services, Ingresses, PVCs): enough for ownership indexing and pruning.
// convertService projects a Service with its live selector: the kernel reads
// the serving color of a blue-green application from it, so the switch
// decision rests on what the cluster routes today, never on intent.
func convertService(raw any) (Object, bool) {
	service, ok := raw.(*corev1.Service)
	if !ok {
		return Object{}, false
	}
	environment, key, revision := identity(service)
	return Object{
		Ref: kube.ObjectRef{
			GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Service"},
			Namespace: service.Namespace, Name: service.Name, UID: service.UID,
		},
		Kind: module.KindService, Name: service.Name,
		Labels:      service.Labels,
		Environment: environment, Service: key, Revision: revision,
		Color:      service.Spec.Selector[rendering.LabelColor],
		Selector:   service.Spec.Selector,
		Generation: service.Generation,
	}, true
}

func convertPlain(gvk schema.GroupVersionKind, kind string) func(any) (Object, bool) {
	return func(raw any) (Object, bool) {
		meta, ok := raw.(metav1.Object)
		if !ok {
			return Object{}, false
		}
		environment, service, revision := identity(meta)
		return Object{
			Ref: kube.ObjectRef{
				GVK:       gvk,
				Namespace: meta.GetNamespace(), Name: meta.GetName(), UID: meta.GetUID(),
			},
			Kind: kind, Name: meta.GetName(),
			Labels:      meta.GetLabels(),
			Environment: environment, Service: service, Revision: revision,
			Generation: meta.GetGeneration(),
		}, true
	}
}
