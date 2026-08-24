package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/dbstore"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// Storage moves slowly, so its samples ride a slower cadence than the 30s
// usage tick, with a timeout sized for one kubelet proxy per node plus one
// scrape per pool.
const (
	storageInterval = 5 * time.Minute
	storageTimeout  = 60 * time.Second
)

// Storage sample kinds; the console groups the per-service breakdown on
// them.
const (
	kindVolume   = "volume"
	kindDatabase = "database"
	kindBucket   = "bucket"
)

// databaseSizeFamily is CNPG's stock per-database size gauge, served by the
// built-in exporter behind each pool's metrics Service.
const databaseSizeFamily = "cnpg_pg_database_size_bytes"

// nodeStorage accumulates one node's sample row.
type nodeStorage struct {
	capacity, used, available          int64
	volumes, databases, objects, image int64
}

// serviceStorage accumulates one service's sample row. used stays nil when
// no real reading exists (local-path app volumes); capacity is the declared
// size or quota.
type serviceStorage struct {
	environment uuid.UUID
	serviceKey  string
	kind        string
	used        *int64
	capacity    int64
}

// sampleStorage collects the storage sample set: node filesystem totals and
// category rollups from the kubelets, per-service footprints from managed
// claims, the pools' exporters, and the object store's master. The four
// collectors degrade independently: a missing source loses its numbers for
// one interval, never the pass.
func (s *Sampler) sampleStorage(ctx context.Context, now time.Time) error {
	nodes := map[string]*nodeStorage{}
	var nodeOrder []string
	node := func(name string) *nodeStorage {
		row := nodes[name]
		if row == nil {
			row = &nodeStorage{}
			nodes[name] = row
			nodeOrder = append(nodeOrder, name)
		}
		return row
	}
	var services []serviceStorage

	pvcUsage, err := s.collectKubeletStats(ctx, node)
	if err != nil {
		return err
	}
	volumeRows, err := s.collectAppVolumes(ctx, node, pvcUsage)
	if err != nil {
		slog.WarnContext(ctx, "sample volume storage", "err", err)
	} else {
		services = append(services, volumeRows...)
	}
	if s.DB != nil {
		databaseRows, err := s.collectDatabaseSizes(ctx, node)
		if err != nil {
			slog.WarnContext(ctx, "sample database storage", "err", err)
		} else {
			services = append(services, databaseRows...)
		}
	}
	if s.DB != nil && s.Seaweed != nil {
		bucketRows, err := s.collectBucketSizes(ctx, node)
		if err != nil {
			slog.WarnContext(ctx, "sample bucket storage", "err", err)
		} else {
			services = append(services, bucketRows...)
		}
	}

	if len(nodeOrder) > 0 {
		params := store.InsertStorageNodeSamplesParams{SampledAt: now}
		for _, name := range nodeOrder {
			row := nodes[name]
			params.NodeNames = append(params.NodeNames, name)
			params.CapacityBytes = append(params.CapacityBytes, row.capacity)
			params.UsedBytes = append(params.UsedBytes, row.used)
			params.AvailableBytes = append(params.AvailableBytes, row.available)
			params.VolumesBytes = append(params.VolumesBytes, row.volumes)
			params.DatabasesBytes = append(params.DatabasesBytes, row.databases)
			params.ObjectsBytes = append(params.ObjectsBytes, row.objects)
			params.ImagesBytes = append(params.ImagesBytes, row.image)
		}
		if _, err := s.Store.InsertStorageNodeSamples(ctx, params); err != nil {
			return fmt.Errorf("insert storage node samples: %w", err)
		}
	}
	if len(services) > 0 {
		params := store.InsertStorageSamplesParams{SampledAt: now}
		for _, row := range services {
			params.EnvironmentIds = append(params.EnvironmentIds, row.environment)
			params.ServiceKeys = append(params.ServiceKeys, row.serviceKey)
			params.Kinds = append(params.Kinds, row.kind)
			measured := row.used != nil
			used := int64(0)
			if measured {
				used = *row.used
			}
			params.UsedBytes = append(params.UsedBytes, used)
			params.UsedMeasured = append(params.UsedMeasured, measured)
			params.CapacityBytes = append(params.CapacityBytes, row.capacity)
		}
		if _, err := s.Store.InsertStorageSamples(ctx, params); err != nil {
			return fmt.Errorf("insert storage samples: %w", err)
		}
	}
	return nil
}

// pvcRef keys a claim across the kubelet stats and the claim list.
type pvcRef struct {
	namespace, name string
}

// pvcReading is one measured claim mount: real bytes and the node the
// consuming pod runs on.
type pvcReading struct {
	usedBytes int64
	node      string
}

// collectKubeletStats reads every node's /stats/summary: filesystem and
// image totals per node, and the per-claim readings that survive the
// degenerate-value filter. A node whose kubelet does not answer keeps its
// other category numbers and simply misses from this interval's node rows.
func (s *Sampler) collectKubeletStats(ctx context.Context, node func(string) *nodeStorage) (map[pvcRef]pvcReading, error) {
	list, err := s.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	readings := map[pvcRef]pvcReading{}
	for _, item := range list.Items {
		body, status, err := s.Kube.NodeProxyDo(ctx, http.MethodGet, item.Name, "/stats/summary", nil, nil)
		if err != nil {
			slog.WarnContext(ctx, "kubelet stats", "node", item.Name, "err", err)
			continue
		}
		if status != http.StatusOK {
			slog.WarnContext(ctx, "kubelet stats", "node", item.Name, "status", status)
			continue
		}
		summary, err := parseStatsSummary(body)
		if err != nil {
			slog.WarnContext(ctx, "parse kubelet stats", "node", item.Name, "err", err)
			continue
		}
		row := node(item.Name)
		row.capacity = summary.capacityBytes
		row.used = summary.usedBytes
		row.available = summary.availableBytes
		row.image = summary.imageFsUsedBytes
		for ref, used := range summary.volumes {
			readings[ref] = pvcReading{usedBytes: used, node: item.Name}
		}
	}
	return readings, nil
}

// statsSummary is the reduced kubelet answer.
type statsSummary struct {
	capacityBytes, usedBytes, availableBytes int64
	imageFsUsedBytes                         int64
	// volumes holds per-claim usage, degenerate entries already dropped.
	volumes map[pvcRef]int64
}

// wire shapes of the kubelet summary endpoint, reduced to what we read.
type summaryDocument struct {
	Node struct {
		Fs      *fsStats `json:"fs"`
		Runtime struct {
			ImageFs *fsStats `json:"imageFs"`
		} `json:"runtime"`
	} `json:"node"`
	Pods []struct {
		Volume []struct {
			fsStats
			PvcRef *struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"pvcRef"`
		} `json:"volume"`
	} `json:"pods"`
}

type fsStats struct {
	CapacityBytes  uint64 `json:"capacityBytes"`
	UsedBytes      uint64 `json:"usedBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
}

// parseStatsSummary reduces one kubelet summary. Claims whose reported
// capacity equals the node filesystem's are dropped: hostPath-backed
// provisioners (local-path) answer statfs of the whole disk for every
// claim, and those numbers are noise, not usage. CSI volumes (Longhorn)
// report their own filesystem and pass.
func parseStatsSummary(body []byte) (statsSummary, error) {
	var document summaryDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return statsSummary{}, err
	}
	summary := statsSummary{volumes: map[pvcRef]int64{}}
	if document.Node.Fs == nil {
		return summary, errors.New("kubelet summary carries no node filesystem stats")
	}
	summary.capacityBytes = int64(document.Node.Fs.CapacityBytes)
	summary.usedBytes = int64(document.Node.Fs.UsedBytes)
	summary.availableBytes = int64(document.Node.Fs.AvailableBytes)
	if document.Node.Runtime.ImageFs != nil {
		summary.imageFsUsedBytes = int64(document.Node.Runtime.ImageFs.UsedBytes)
	}
	for _, pod := range document.Pods {
		for _, volume := range pod.Volume {
			if volume.PvcRef == nil || volume.CapacityBytes == 0 {
				continue
			}
			if volume.CapacityBytes == document.Node.Fs.CapacityBytes {
				continue
			}
			ref := pvcRef{namespace: volume.PvcRef.Namespace, name: volume.PvcRef.Name}
			summary.volumes[ref] += int64(volume.UsedBytes)
		}
	}
	return summary, nil
}

// collectAppVolumes joins the managed claims onto the kubelet readings: one
// row per (environment, application service), declared sizes summed as
// capacity, real usage only where a reading survived the degenerate filter.
// Measured usage also lands in the mounting node's volumes category.
func (s *Sampler) collectAppVolumes(ctx context.Context, node func(string) *nodeStorage, readings map[pvcRef]pvcReading) ([]serviceStorage, error) {
	list, err := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(metav1.NamespaceAll).
		List(ctx, metav1.ListOptions{LabelSelector: rendering.ManagedSelector})
	if err != nil {
		return nil, fmt.Errorf("list volume claims: %w", err)
	}
	type volumeAgg struct {
		capacity int64
		used     int64
		measured bool
	}
	type serviceKey struct {
		env     uuid.UUID
		service string
	}
	agg := map[serviceKey]*volumeAgg{}
	var order []serviceKey
	for _, claim := range list.Items {
		labels := claim.GetLabels()
		envID, err := uuid.Parse(labels[rendering.LabelEnvironment])
		if err != nil {
			continue
		}
		service := labels[rendering.LabelService]
		if service == "" {
			continue
		}
		key := serviceKey{env: envID, service: service}
		row := agg[key]
		if row == nil {
			row = &volumeAgg{}
			agg[key] = row
			order = append(order, key)
		}
		if request, ok := claim.Spec.Resources.Requests["storage"]; ok {
			row.capacity += request.Value()
		}
		reading, ok := readings[pvcRef{namespace: claim.Namespace, name: claim.Name}]
		if !ok {
			continue
		}
		row.used += reading.usedBytes
		row.measured = true
		node(reading.node).volumes += reading.usedBytes
	}
	rows := make([]serviceStorage, 0, len(order))
	for _, key := range order {
		row := agg[key]
		entry := serviceStorage{
			environment: key.env, serviceKey: key.service,
			kind: kindVolume, capacity: row.capacity,
		}
		if row.measured {
			used := row.used
			entry.used = &used
		}
		rows = append(rows, entry)
	}
	return rows, nil
}

// collectDatabaseSizes scrapes every pool's exporter Service for the stock
// per-database size gauge. Service-owned tenant databases become rows under
// their environment; the pool's full total (system databases and templates
// included) charges every node holding one of its instances, because each
// instance is a complete copy.
func (s *Sampler) collectDatabaseSizes(ctx context.Context, node func(string) *nodeStorage) ([]serviceStorage, error) {
	pools, err := s.DB.ListLiveClusters(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pools: %w", err)
	}
	if len(pools) == 0 {
		return nil, nil
	}
	var rows []serviceStorage
	poolTotals := map[string]int64{}
	for _, pool := range pools {
		body, status, err := s.Kube.ServiceProxyDo(ctx, http.MethodGet, s.PlatformNamespace,
			cnpg.MetricsServiceName(pool.Name), cnpg.MetricsPort, "/metrics", nil, nil)
		if err != nil {
			slog.WarnContext(ctx, "pool metrics scrape", "pool", pool.Name, "err", err)
			continue
		}
		if status != http.StatusOK {
			// Expected until the pool's metrics Service exists (first
			// converge after this release) or while the primary is moving.
			if !s.poolMetricsUnavailable[pool.Name] {
				if s.poolMetricsUnavailable == nil {
					s.poolMetricsUnavailable = map[string]bool{}
				}
				s.poolMetricsUnavailable[pool.Name] = true
				slog.InfoContext(ctx, "pool metrics endpoint unavailable", "pool", pool.Name, "status", status)
			}
			continue
		}
		delete(s.poolMetricsUnavailable, pool.Name)
		sizes, err := parseGaugeByLabel(body, databaseSizeFamily, "datname")
		if err != nil {
			slog.WarnContext(ctx, "parse pool metrics", "pool", pool.Name, "err", err)
			continue
		}
		total := int64(0)
		for _, size := range sizes {
			total += size
		}
		poolTotals[pool.Name] = total

		tenants, err := s.DB.ListClusterTenants(ctx, pool.ID)
		if err != nil {
			return nil, fmt.Errorf("list pool tenants: %w", err)
		}
		for _, tenant := range tenants {
			size, ok := sizes[tenant.DatabaseName]
			if !ok {
				continue
			}
			claim, err := s.DB.GetClaim(ctx, tenant.ClaimID)
			if err != nil {
				if errors.Is(err, dbstore.ErrNotFound) {
					continue
				}
				return nil, fmt.Errorf("read claim: %w", err)
			}
			if claim.OwnerKind != dbstore.OwnerService || claim.EnvironmentID == nil {
				continue
			}
			used := size
			rows = append(rows, serviceStorage{
				environment: *claim.EnvironmentID,
				serviceKey:  "databases." + claim.ServiceKey,
				kind:        kindDatabase,
				used:        &used,
				capacity:    claim.StorageBytes,
			})
		}
	}
	if len(poolTotals) > 0 {
		pods, err := s.Kube.Clientset.CoreV1().Pods(s.PlatformNamespace).
			List(ctx, metav1.ListOptions{LabelSelector: "cnpg.io/podRole=instance"})
		if err != nil {
			return rows, fmt.Errorf("list pool instances: %w", err)
		}
		for _, pod := range pods.Items {
			total, ok := poolTotals[pod.Labels["cnpg.io/cluster"]]
			if !ok || pod.Spec.NodeName == "" {
				continue
			}
			node(pod.Spec.NodeName).databases += total
		}
	}
	return rows, nil
}

// collectBucketSizes mirrors the substrate's bucket probe joins: collection
// sizes from the master keyed through claims and allocations, one row per
// service-owned bucket. Node attribution reads the volume servers' physical
// bytes and maps each server URL onto its node through the pod IPs.
func (s *Sampler) collectBucketSizes(ctx context.Context, node func(string) *nodeStorage) ([]serviceStorage, error) {
	storeRow, err := s.DB.LiveObjectStore(ctx)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("read object store: %w", err)
	}
	sizes, err := s.Seaweed.CollectionSizes(ctx)
	if err != nil {
		return nil, fmt.Errorf("collection sizes: %w", err)
	}
	claims, err := s.DB.ListStoreBucketClaims(ctx, storeRow.ID)
	if err != nil {
		return nil, fmt.Errorf("list bucket claims: %w", err)
	}
	var rows []serviceStorage
	for _, claim := range claims {
		allocation, err := s.DB.LiveAllocation(ctx, claim.ID)
		if err != nil {
			if errors.Is(err, dbstore.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("read allocation: %w", err)
		}
		if claim.OwnerKind != dbstore.OwnerService || claim.EnvironmentID == nil {
			continue
		}
		used := sizes[allocation.BucketName].SizeBytes
		rows = append(rows, serviceStorage{
			environment: *claim.EnvironmentID,
			serviceKey:  "buckets." + claim.ServiceKey,
			kind:        kindBucket,
			used:        &used,
			capacity:    claim.StorageQuotaBytes,
		})
	}

	byServer, err := s.Seaweed.VolumeSizesByNode(ctx)
	if err != nil {
		return rows, fmt.Errorf("volume server sizes: %w", err)
	}
	if len(byServer) > 0 {
		pods, err := s.Kube.Clientset.CoreV1().Pods(s.PlatformNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return rows, fmt.Errorf("list platform pods: %w", err)
		}
		podNode := map[string]string{}
		for _, pod := range pods.Items {
			if pod.Status.PodIP != "" && pod.Spec.NodeName != "" {
				podNode[pod.Status.PodIP] = pod.Spec.NodeName
			}
		}
		for server, bytesOnNode := range byServer {
			host := server
			if index := strings.LastIndexByte(host, ':'); index >= 0 {
				host = host[:index]
			}
			if name, ok := podNode[host]; ok {
				node(name).objects += bytesOnNode
			}
		}
	}
	return rows, nil
}

// parseGaugeByLabel reduces a Prometheus text exposition to one family's
// gauge values keyed by a label.
func parseGaugeByLabel(body []byte, family, label string) (map[string]int64, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	values := map[string]int64{}
	mf, ok := families[family]
	if !ok {
		return values, nil
	}
	for _, metric := range mf.GetMetric() {
		key := ""
		for _, pair := range metric.GetLabel() {
			if pair.GetName() == label {
				key = pair.GetValue()
				break
			}
		}
		if key == "" {
			continue
		}
		values[key] = int64(math.Round(metric.GetGauge().GetValue()))
	}
	return values, nil
}
