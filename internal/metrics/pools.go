package metrics

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// The CNPG exporter families the pool sampler reads, from the operator's
// default monitoring queries (cnpg_<query>_<column>). Backends is a gauge;
// the pg_stat_database columns are lifetime counters differenced per tick.
const (
	familyBackends     = "cnpg_backends_total"
	familyXactCommit   = "cnpg_pg_stat_database_xact_commit"
	familyXactRollback = "cnpg_pg_stat_database_xact_rollback"
	familyBlksHit      = "cnpg_pg_stat_database_blks_hit"
	familyBlksRead     = "cnpg_pg_stat_database_blks_read"
)

// poolCounters are the lifetime counters of one exporter reading, summed
// over every database.
type poolCounters struct {
	commit, rollback, blksHit, blksRead int64
}

// delta subtracts the previous reading; ok is false when any counter went
// backwards (a stats reset, or a failover moving the scrape onto an
// instance with its own counter lifetime), in which case the interval is
// a gap rather than a spike.
func (c poolCounters) delta(prev poolCounters) (poolCounters, bool) {
	out := poolCounters{
		commit:   c.commit - prev.commit,
		rollback: c.rollback - prev.rollback,
		blksHit:  c.blksHit - prev.blksHit,
		blksRead: c.blksRead - prev.blksRead,
	}
	if out.commit < 0 || out.rollback < 0 || out.blksHit < 0 || out.blksRead < 0 {
		return poolCounters{}, false
	}
	return out, true
}

// poolExporterReading is one scrape of a pool's exporter reduced to the
// pool-wide numbers the sampler stores.
type poolExporterReading struct {
	connections   int64
	counters      poolCounters
	databaseBytes int64
}

// parsePoolExporter reduces a CNPG exporter exposition to pool totals: every
// family summed over all its label sets (databases, users, states).
func parsePoolExporter(body []byte) (poolExporterReading, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return poolExporterReading{}, err
	}
	sum := func(name string) int64 {
		mf, ok := families[name]
		if !ok {
			return 0
		}
		total := 0.0
		for _, metric := range mf.GetMetric() {
			switch mf.GetType() {
			case dto.MetricType_COUNTER:
				total += metric.GetCounter().GetValue()
			default:
				total += metric.GetGauge().GetValue()
			}
		}
		return int64(math.Round(total))
	}
	return poolExporterReading{
		connections: sum(familyBackends),
		counters: poolCounters{
			commit:   sum(familyXactCommit),
			rollback: sum(familyXactRollback),
			blksHit:  sum(familyBlksHit),
			blksRead: sum(familyBlksRead),
		},
		databaseBytes: sum(databaseSizeFamily),
	}, nil
}

// poolPodUsage sums instance PodMetrics per pool (the cnpg.io/cluster
// label) and counts the instances that reported.
type poolPodUsage struct {
	cpu, mem, pods int64
}

func poolUsageByCluster(items []unstructuredItem) map[string]*poolPodUsage {
	usage := map[string]*poolPodUsage{}
	for _, item := range items {
		pool := item.labels[cnpg.LabelCluster]
		if pool == "" {
			continue
		}
		row := usage[pool]
		if row == nil {
			row = &poolPodUsage{}
			usage[pool] = row
		}
		row.cpu += item.cpu
		row.mem += item.mem
		row.pods++
	}
	return usage
}

// unstructuredItem is one PodMetrics item reduced to what pool grouping
// needs; a small seam so the grouping is testable without a dynamic client.
type unstructuredItem struct {
	labels   map[string]string
	cpu, mem int64
}

// samplePools appends one row per live pool: the instances' summed
// PodMetrics and ready count plus the primary's exporter reading. Pools
// are few, so rows insert one at a time; a failed exporter scrape stores
// NULL exporter columns rather than dropping the pod side.
func (s *Sampler) samplePools(ctx context.Context, now time.Time) error {
	if s.DB == nil {
		return nil
	}
	pools, err := s.DB.ListLiveClusters(ctx)
	if err != nil {
		return fmt.Errorf("list pools: %w", err)
	}
	if len(pools) == 0 {
		s.lastPool = nil
		return nil
	}
	selector := cnpg.LabelPodRole + "=" + cnpg.PodRoleInstance
	list, err := s.Kube.Dynamic.Resource(podMetricsGVR).Namespace(s.PlatformNamespace).
		List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list pool pod metrics: %w", err)
	}
	items := make([]unstructuredItem, 0, len(list.Items))
	for _, item := range list.Items {
		cpu, mem, err := podUsage(item)
		if err != nil {
			slog.WarnContext(ctx, "parse pod metrics", "pod", item.GetName(), "err", err)
			continue
		}
		items = append(items, unstructuredItem{labels: item.GetLabels(), cpu: cpu, mem: mem})
	}
	usage := poolUsageByCluster(items)

	pods, err := s.Kube.Clientset.CoreV1().Pods(s.PlatformNamespace).
		List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list pool instances: %w", err)
	}
	// Ready counts keyed by each pod's cluster label.
	ready := map[string]int64{}
	for _, pod := range pods.Items {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				ready[pod.Labels[cnpg.LabelCluster]]++
			}
		}
	}

	if s.lastPool == nil {
		s.lastPool = map[uuid.UUID]poolCounters{}
	}
	live := map[uuid.UUID]bool{}
	for _, pool := range pools {
		live[pool.ID] = true
		params := store.InsertPoolMetricSampleParams{
			ClusterID:      pool.ID,
			SampledAt:      now,
			InstancesReady: ready[pool.Name],
		}
		if row := usage[pool.Name]; row != nil {
			params.CpuMillicores, params.MemoryBytes, params.Instances = row.cpu, row.mem, row.pods
		}
		if reading, ok := s.scrapePoolExporter(ctx, pool.Name); ok {
			connections, size := reading.connections, reading.databaseBytes
			params.Connections = &connections
			params.DatabaseBytes = &size
			if prev, seen := s.lastPool[pool.ID]; seen {
				if delta, ok := reading.counters.delta(prev); ok {
					commit, rollback, hit, read := delta.commit, delta.rollback, delta.blksHit, delta.blksRead
					params.XactCommit, params.XactRollback, params.BlksHit, params.BlksRead = &commit, &rollback, &hit, &read
				} else {
					slog.DebugContext(ctx, "pool counters reset", "pool", pool.Name)
				}
			}
			s.lastPool[pool.ID] = reading.counters
		}
		if err := s.Store.InsertPoolMetricSample(ctx, params); err != nil {
			return fmt.Errorf("insert pool sample %s: %w", pool.Name, err)
		}
	}
	for id := range s.lastPool {
		if !live[id] {
			delete(s.lastPool, id)
		}
	}
	return nil
}

// scrapePoolExporter reads one pool's exporter Service; ok is false when
// the Service is not applied yet or the scrape failed (logged once per
// outage, like the storage collector).
func (s *Sampler) scrapePoolExporter(ctx context.Context, pool string) (poolExporterReading, bool) {
	body, status, err := s.Kube.ServiceProxyDo(ctx, http.MethodGet, s.PlatformNamespace,
		cnpg.MetricsServiceName(pool), cnpg.MetricsPort, "/metrics", nil, nil)
	if err != nil {
		slog.WarnContext(ctx, "pool metrics scrape", "pool", pool, "err", err)
		return poolExporterReading{}, false
	}
	if status != http.StatusOK {
		if !s.poolMetricsUnavailable[pool] {
			if s.poolMetricsUnavailable == nil {
				s.poolMetricsUnavailable = map[string]bool{}
			}
			s.poolMetricsUnavailable[pool] = true
			slog.InfoContext(ctx, "pool metrics endpoint unavailable", "pool", pool, "status", status)
		}
		return poolExporterReading{}, false
	}
	delete(s.poolMetricsUnavailable, pool)
	reading, err := parsePoolExporter(body)
	if err != nil {
		slog.WarnContext(ctx, "parse pool metrics", "pool", pool, "err", err)
		return poolExporterReading{}, false
	}
	return reading, true
}
