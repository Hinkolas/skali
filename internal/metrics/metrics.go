// Package metrics stores and serves usage telemetry. A sampler loop in
// skalid reads instantaneous usage from metrics.k8s.io (metrics-server, part
// of every skali cluster) and appends per-application and per-node samples
// to the platform database; the Service reads them back as fixed-step
// bucketed series for the console charts. Samples are observability data
// only: nothing in reconciliation ever reads them.
package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/store"
)

// Service serves bucketed series from stored samples. It needs only the
// database, so the metrics API stays available in API-only mode (the sampler
// simply never writes there).
type Service struct {
	Store *store.Store
}

// Window is one fixed query range. Steps are chosen so every window renders
// a bounded point count (60 to 168) regardless of retention.
type Window struct {
	Name string
	Step time.Duration
	Span time.Duration
}

var windows = []Window{
	{Name: "1h", Step: time.Minute, Span: time.Hour},
	{Name: "24h", Step: 15 * time.Minute, Span: 24 * time.Hour},
	{Name: "7d", Step: time.Hour, Span: 7 * 24 * time.Hour},
}

// WindowByName resolves a window parameter; the empty string selects the
// 24h default.
func WindowByName(name string) (Window, error) {
	if name == "" {
		name = "24h"
	}
	for _, w := range windows {
		if w.Name == name {
			return w, nil
		}
	}
	return Window{}, fmt.Errorf("metrics: unknown window %q", name)
}

// grid computes the aligned bucket timestamps for a window ending at now.
// The trailing bucket is the in-progress one, so the newest point reflects
// current usage instead of lagging a full step.
func (w Window) grid(now time.Time) (since, until time.Time, timestamps []time.Time) {
	until = now.UTC().Truncate(w.Step).Add(w.Step)
	since = until.Add(-w.Span)
	n := int(w.Span / w.Step)
	timestamps = make([]time.Time, n)
	for i := range timestamps {
		timestamps[i] = since.Add(time.Duration(i) * w.Step)
	}
	return since, until, timestamps
}

// bucketIndex places a date_bin bucket (anchored at since) on the grid.
func bucketIndex(since, bucket time.Time, step time.Duration, n int) (int, bool) {
	i := int(bucket.Sub(since) / step)
	return i, i >= 0 && i < n
}

// ApplicationSeries carries one application's aligned series; nil entries
// are buckets without samples and render as chart gaps.
type ApplicationSeries struct {
	Key           string
	CPUMillicores []*int64
	MemoryBytes   []*int64
	// Edge is nil until edge samples exist for the application.
	Edge *EdgeSeries
}

// EdgeSeries is the per-bucket edge traffic of one application: request
// counts and bytes that arrived within each bucket (sums of stored deltas).
type EdgeSeries struct {
	Requests      []*int64
	RequestBytes  []*int64
	ResponseBytes []*int64
}

type EnvironmentSeries struct {
	Window     Window
	Timestamps []time.Time
	Apps       []ApplicationSeries
}

func (s *Service) EnvironmentSeries(ctx context.Context, environmentID uuid.UUID, w Window, now time.Time) (*EnvironmentSeries, error) {
	since, until, timestamps := w.grid(now)
	rows, err := s.Store.AppMetricSeries(ctx, store.AppMetricSeriesParams{
		EnvironmentID: environmentID,
		StepSeconds:   int32(w.Step / time.Second),
		Origin:        since,
		Since:         since,
		Until:         until,
	})
	if err != nil {
		return nil, err
	}
	series := &EnvironmentSeries{Window: w, Timestamps: timestamps}
	appAt := make(map[string]int)
	appFor := func(key string) *ApplicationSeries {
		if i, ok := appAt[key]; ok {
			return &series.Apps[i]
		}
		series.Apps = append(series.Apps, ApplicationSeries{
			Key:           key,
			CPUMillicores: make([]*int64, len(timestamps)),
			MemoryBytes:   make([]*int64, len(timestamps)),
		})
		appAt[key] = len(series.Apps) - 1
		return &series.Apps[len(series.Apps)-1]
	}
	for _, row := range rows {
		app := appFor(row.ApplicationKey)
		i, ok := bucketIndex(since, row.Bucket, w.Step, len(timestamps))
		if !ok {
			continue
		}
		cpu, mem := row.CpuMillicores, row.MemoryBytes
		app.CPUMillicores[i] = &cpu
		app.MemoryBytes[i] = &mem
	}

	edgeRows, err := s.Store.EdgeMetricSeries(ctx, store.EdgeMetricSeriesParams{
		EnvironmentID: environmentID,
		StepSeconds:   int32(w.Step / time.Second),
		Origin:        since,
		Since:         since,
		Until:         until,
	})
	if err != nil {
		return nil, err
	}
	for _, row := range edgeRows {
		app := appFor(row.ApplicationKey)
		if app.Edge == nil {
			app.Edge = &EdgeSeries{
				Requests:      make([]*int64, len(timestamps)),
				RequestBytes:  make([]*int64, len(timestamps)),
				ResponseBytes: make([]*int64, len(timestamps)),
			}
		}
		i, ok := bucketIndex(since, row.Bucket, w.Step, len(timestamps))
		if !ok {
			continue
		}
		requests, requestBytes, responseBytes := row.Requests, row.RequestBytes, row.ResponseBytes
		app.Edge.Requests[i] = &requests
		app.Edge.RequestBytes[i] = &requestBytes
		app.Edge.ResponseBytes[i] = &responseBytes
	}
	return series, nil
}

// NodeSeries carries one node's aligned usage series plus its allocatable
// capacity (from the newest bucket, so a resize shows the current truth).
type NodeSeries struct {
	Name                     string
	CPUAllocatableMillicores int64
	MemoryAllocatableBytes   int64
	CPUMillicores            []*int64
	MemoryBytes              []*int64
}

type NodesSeries struct {
	Window     Window
	Timestamps []time.Time
	Nodes      []NodeSeries
}

// NodeStorage is one node's newest storage sample with its category split.
type NodeStorage struct {
	Name           string
	SampledAt      time.Time
	CapacityBytes  int64
	UsedBytes      int64
	AvailableBytes int64
	VolumesBytes   int64
	DatabasesBytes int64
	ObjectsBytes   int64
	ImagesBytes    int64
	TemporaryBytes int64
}

// NodesStorage serves the current per-node storage picture. The one-hour
// cutoff makes a dead sampler read as "no data" instead of serving stale
// numbers as current.
func (s *Service) NodesStorage(ctx context.Context, now time.Time) ([]NodeStorage, error) {
	rows, err := s.Store.CurrentStorageNodeSamples(ctx, now.Add(-time.Hour))
	if err != nil {
		return nil, err
	}
	nodes := make([]NodeStorage, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, NodeStorage{
			Name:           row.NodeName,
			SampledAt:      row.SampledAt,
			CapacityBytes:  row.CapacityBytes,
			UsedBytes:      row.UsedBytes,
			AvailableBytes: row.AvailableBytes,
			VolumesBytes:   row.VolumesBytes,
			DatabasesBytes: row.DatabasesBytes,
			ObjectsBytes:   row.ObjectsBytes,
			ImagesBytes:    row.ImagesBytes,
			TemporaryBytes: row.TemporaryBytes,
		})
	}
	return nodes, nil
}

// ServiceStorage is one service's newest storage footprint. UsedBytes is
// nil where usage is unmeasurable (local-path app volumes); CapacityBytes
// is the declared size or quota, 0 when unknown.
type ServiceStorage struct {
	EnvironmentID uuid.UUID
	ServiceKey    string
	Kind          string
	UsedBytes     *int64
	CapacityBytes int64
	SampledAt     time.Time
}

// ProjectStorage serves the current per-service storage footprints of one
// project's environments. The 24-hour cutoff tolerates pool scrape gaps
// and scaled-down apps while still aging truly stale rows out.
func (s *Service) ProjectStorage(ctx context.Context, projectID uuid.UUID, now time.Time) ([]ServiceStorage, error) {
	rows, err := s.Store.CurrentProjectStorage(ctx, store.CurrentProjectStorageParams{
		ProjectID: projectID,
		Since:     now.Add(-24 * time.Hour),
	})
	if err != nil {
		return nil, err
	}
	services := make([]ServiceStorage, 0, len(rows))
	for _, row := range rows {
		services = append(services, ServiceStorage{
			EnvironmentID: row.EnvironmentID,
			ServiceKey:    row.ServiceKey,
			Kind:          row.Kind,
			UsedBytes:     row.UsedBytes,
			CapacityBytes: row.CapacityBytes,
			SampledAt:     row.SampledAt,
		})
	}
	return services, nil
}

func (s *Service) NodesSeries(ctx context.Context, w Window, now time.Time) (*NodesSeries, error) {
	since, until, timestamps := w.grid(now)
	rows, err := s.Store.NodeMetricSeries(ctx, store.NodeMetricSeriesParams{
		StepSeconds: int32(w.Step / time.Second),
		Origin:      since,
		Since:       since,
		Until:       until,
	})
	if err != nil {
		return nil, err
	}
	series := &NodesSeries{Window: w, Timestamps: timestamps}
	var current *NodeSeries
	for _, row := range rows {
		if current == nil || current.Name != row.NodeName {
			series.Nodes = append(series.Nodes, NodeSeries{
				Name:          row.NodeName,
				CPUMillicores: make([]*int64, len(timestamps)),
				MemoryBytes:   make([]*int64, len(timestamps)),
			})
			current = &series.Nodes[len(series.Nodes)-1]
		}
		// Rows arrive bucket-ascending per node; the last write wins, which
		// is exactly the newest allocatable.
		current.CPUAllocatableMillicores = row.CpuAllocatableMillicores
		current.MemoryAllocatableBytes = row.MemoryAllocatableBytes
		i, ok := bucketIndex(since, row.Bucket, w.Step, len(timestamps))
		if !ok {
			continue
		}
		cpu, mem := row.CpuMillicores, row.MemoryBytes
		current.CPUMillicores[i] = &cpu
		current.MemoryBytes[i] = &mem
	}
	return series, nil
}
