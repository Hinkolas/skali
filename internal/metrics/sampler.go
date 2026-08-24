package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

var (
	podMetricsGVR  = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	nodeMetricsGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
)

const (
	defaultInterval  = 30 * time.Second
	defaultRetention = 8 * 24 * time.Hour
	tickTimeout      = 15 * time.Second
	pruneEvery       = time.Hour
)

// Sampler appends usage samples on a fixed cadence. Failures are logged and
// the next tick retries; a broken metrics pipeline degrades charts, never
// the platform.
type Sampler struct {
	Store *store.Store
	Kube  *kube.Client
	// DB resolves storage attribution (pools, tenants, claims, buckets);
	// nil skips the database and bucket storage collectors.
	DB *dbstore.Service
	// Seaweed reads the object store's volume listing; nil skips the
	// bucket storage collector.
	Seaweed *seaweed.Client
	// PlatformNamespace is where pools and seaweed live
	// (substrate.Namespace); required for the storage collectors.
	PlatformNamespace string
	// Interval between samples; zero selects the 30s default.
	Interval time.Duration
	// Retention is the sample age cutoff; zero selects the 8 day default.
	Retention time.Duration

	lastPrune   time.Time
	lastStorage time.Time
	// poolMetricsUnavailable suppresses repeat logging per pool while its
	// metrics Service is not applied yet.
	poolMetricsUnavailable map[string]bool
	// lastEdge holds the previous scrape's cumulative counters per Traefik
	// router; deltas against it become the stored edge samples.
	lastEdge map[string]routerCounters
	// edgeUnavailable suppresses repeat logging while the Traefik metrics
	// port is not exposed yet.
	edgeUnavailable bool
}

func (s *Sampler) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	s.tick(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Sampler) tick(ctx context.Context) {
	tickCtx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.sampleApps(tickCtx, now); err != nil && !errors.Is(err, context.Canceled) {
		slog.WarnContext(ctx, "sample application metrics", "err", err)
	}
	if err := s.sampleNodes(tickCtx, now); err != nil && !errors.Is(err, context.Canceled) {
		slog.WarnContext(ctx, "sample node metrics", "err", err)
	}
	if err := s.sampleEdge(tickCtx, now); err != nil && !errors.Is(err, context.Canceled) {
		slog.WarnContext(ctx, "sample edge metrics", "err", err)
	}
	// Storage rides its own slower cadence and a wider timeout: one
	// kubelet proxy per node plus one scrape per pool. Time-based rather
	// than tick-counted, so a restart samples immediately.
	if now.Sub(s.lastStorage) >= storageInterval {
		s.lastStorage = now
		storageCtx, cancelStorage := context.WithTimeout(ctx, storageTimeout)
		if err := s.sampleStorage(storageCtx, now); err != nil && !errors.Is(err, context.Canceled) {
			slog.WarnContext(ctx, "sample storage metrics", "err", err)
		}
		cancelStorage()
	}
	if now.Sub(s.lastPrune) >= pruneEvery {
		s.lastPrune = now
		s.prune(tickCtx, now)
	}
}

// sampleApps aggregates PodMetrics into one row per (environment,
// application). metrics-server mirrors pod labels onto PodMetrics, so the
// managed selector scopes the list and the identity labels place each pod;
// stateful substrate pods carry no application label and fall out naturally.
func (s *Sampler) sampleApps(ctx context.Context, now time.Time) error {
	list, err := s.Kube.Dynamic.Resource(podMetricsGVR).Namespace(metav1.NamespaceAll).
		List(ctx, metav1.ListOptions{LabelSelector: rendering.ManagedSelector})
	if err != nil {
		return fmt.Errorf("list pod metrics: %w", err)
	}
	type appKey struct {
		env uuid.UUID
		app string
	}
	type usage struct {
		cpu, mem, pods int64
	}
	agg := make(map[appKey]*usage)
	var order []appKey
	for _, item := range list.Items {
		labels := item.GetLabels()
		envID, err := uuid.Parse(labels[rendering.LabelEnvironment])
		if err != nil {
			continue
		}
		app := labels[rendering.LabelApplication]
		if app == "" || rendering.IsReleaseServiceIdentity(labels[rendering.LabelService]) {
			continue
		}
		cpu, mem, err := podUsage(item)
		if err != nil {
			slog.WarnContext(ctx, "parse pod metrics", "pod", item.GetName(), "err", err)
			continue
		}
		key := appKey{env: envID, app: app}
		row := agg[key]
		if row == nil {
			row = &usage{}
			agg[key] = row
			order = append(order, key)
		}
		row.cpu += cpu
		row.mem += mem
		row.pods++
	}
	if len(order) == 0 {
		return nil
	}
	params := store.InsertAppMetricSamplesParams{SampledAt: now}
	for _, key := range order {
		row := agg[key]
		params.EnvironmentIds = append(params.EnvironmentIds, key.env)
		params.ApplicationKeys = append(params.ApplicationKeys, key.app)
		params.CpuMillicores = append(params.CpuMillicores, row.cpu)
		params.MemoryBytes = append(params.MemoryBytes, row.mem)
		params.PodCounts = append(params.PodCounts, row.pods)
	}
	if _, err := s.Store.InsertAppMetricSamples(ctx, params); err != nil {
		return fmt.Errorf("insert app samples: %w", err)
	}
	return nil
}

// podUsage sums container usage quantities of one PodMetrics object.
func podUsage(item unstructured.Unstructured) (cpuMillicores, memoryBytes int64, err error) {
	containers, _, err := unstructured.NestedSlice(item.Object, "containers")
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range containers {
		container, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		usage, _, err := unstructured.NestedStringMap(container, "usage")
		if err != nil {
			return 0, 0, err
		}
		cpu, mem, err := parseUsage(usage)
		if err != nil {
			return 0, 0, err
		}
		cpuMillicores += cpu
		memoryBytes += mem
	}
	return cpuMillicores, memoryBytes, nil
}

func parseUsage(usage map[string]string) (cpuMillicores, memoryBytes int64, err error) {
	if raw, ok := usage["cpu"]; ok {
		q, err := resource.ParseQuantity(raw)
		if err != nil {
			return 0, 0, fmt.Errorf("cpu %q: %w", raw, err)
		}
		cpuMillicores = q.MilliValue()
	}
	if raw, ok := usage["memory"]; ok {
		q, err := resource.ParseQuantity(raw)
		if err != nil {
			return 0, 0, fmt.Errorf("memory %q: %w", raw, err)
		}
		memoryBytes = q.Value()
	}
	return cpuMillicores, memoryBytes, nil
}

// sampleNodes joins NodeMetrics usage with the node list's allocatable so
// every sample carries its own denominator.
func (s *Sampler) sampleNodes(ctx context.Context, now time.Time) error {
	list, err := s.Kube.Dynamic.Resource(nodeMetricsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list node metrics: %w", err)
	}
	nodes, err := s.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	type capacity struct {
		cpu, mem int64
	}
	alloc := make(map[string]capacity, len(nodes.Items))
	for _, node := range nodes.Items {
		alloc[node.Name] = capacity{
			cpu: node.Status.Allocatable.Cpu().MilliValue(),
			mem: node.Status.Allocatable.Memory().Value(),
		}
	}
	params := store.InsertNodeMetricSamplesParams{SampledAt: now}
	for _, item := range list.Items {
		usage, _, err := unstructured.NestedStringMap(item.Object, "usage")
		if err != nil {
			continue
		}
		cpu, mem, err := parseUsage(usage)
		if err != nil {
			slog.WarnContext(ctx, "parse node metrics", "node", item.GetName(), "err", err)
			continue
		}
		cap := alloc[item.GetName()]
		params.NodeNames = append(params.NodeNames, item.GetName())
		params.CpuMillicores = append(params.CpuMillicores, cpu)
		params.MemoryBytes = append(params.MemoryBytes, mem)
		params.CpuAllocatableMillicores = append(params.CpuAllocatableMillicores, cap.cpu)
		params.MemoryAllocatableBytes = append(params.MemoryAllocatableBytes, cap.mem)
	}
	if len(params.NodeNames) == 0 {
		return nil
	}
	if _, err := s.Store.InsertNodeMetricSamples(ctx, params); err != nil {
		return fmt.Errorf("insert node samples: %w", err)
	}
	return nil
}

func (s *Sampler) prune(ctx context.Context, now time.Time) {
	retention := s.Retention
	if retention <= 0 {
		retention = defaultRetention
	}
	cutoff := now.Add(-retention)
	apps, err := s.Store.DeleteAgedAppMetricSamples(ctx, cutoff)
	if err != nil {
		slog.WarnContext(ctx, "prune app metric samples", "err", err)
	}
	nodes, err := s.Store.DeleteAgedNodeMetricSamples(ctx, cutoff)
	if err != nil {
		slog.WarnContext(ctx, "prune node metric samples", "err", err)
	}
	edges, err := s.Store.DeleteAgedEdgeMetricSamples(ctx, cutoff)
	if err != nil {
		slog.WarnContext(ctx, "prune edge metric samples", "err", err)
	}
	storageNodes, err := s.Store.DeleteAgedStorageNodeSamples(ctx, cutoff)
	if err != nil {
		slog.WarnContext(ctx, "prune storage node samples", "err", err)
	}
	storageServices, err := s.Store.DeleteAgedStorageSamples(ctx, cutoff)
	if err != nil {
		slog.WarnContext(ctx, "prune storage samples", "err", err)
	}
	if apps+nodes+edges+storageNodes+storageServices > 0 {
		slog.InfoContext(ctx, "pruned metric samples", "apps", apps, "nodes", nodes, "edges", edges,
			"storage_nodes", storageNodes, "storage_services", storageServices)
	}
}
