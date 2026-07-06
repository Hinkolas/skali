package hostinfo

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// RegisterGauges mirrors the sampler's latest snapshot as OTel observable
// gauges. This is the observability side-channel only — skali's own decisions
// and UI read the control-plane path (heartbeat → Postgres). With no OTLP
// endpoint configured, internal/obs installs no MeterProvider, so the global
// meter is a no-op and this costs nothing.
func RegisterGauges(s *Sampler) error {
	meter := otel.Meter("github.com/Hinkolas/skali/internal/hostinfo")

	cpuG, err := meter.Float64ObservableGauge("skali.node.cpu.utilization",
		metric.WithUnit("%"), metric.WithDescription("CPU utilization, 0-100 across effective cores"))
	if err != nil {
		return err
	}
	loadG, err := meter.Float64ObservableGauge("skali.node.load1",
		metric.WithDescription("1-minute load average (host-wide, not namespaced)"))
	if err != nil {
		return err
	}
	ints := make(map[string]metric.Int64ObservableGauge, 8)
	for name, desc := range map[string]string{
		"skali.node.memory.used":        "Working-set memory in use",
		"skali.node.memory.total":       "Memory total (cgroup limit when constrained)",
		"skali.node.disk.used":          "Disk used on the data filesystem",
		"skali.node.disk.total":         "Disk total on the data filesystem",
		"skali.node.network.rx.rate":    "Network receive rate",
		"skali.node.network.tx.rate":    "Network transmit rate",
		"skali.node.disk.io.read.rate":  "Disk read rate",
		"skali.node.disk.io.write.rate": "Disk write rate",
	} {
		g, err := meter.Int64ObservableGauge(name,
			metric.WithUnit("By"), metric.WithDescription(desc))
		if err != nil {
			return err
		}
		ints[name] = g
	}

	observables := make([]metric.Observable, 0, 10)
	observables = append(observables, cpuG, loadG)
	for _, g := range ints {
		observables = append(observables, g)
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		snap, ok := s.Latest()
		if !ok {
			return nil
		}
		o.ObserveFloat64(cpuG, snap.CPUPercent)
		o.ObserveFloat64(loadG, snap.Load1)
		o.ObserveInt64(ints["skali.node.memory.used"], int64(snap.MemUsed))
		o.ObserveInt64(ints["skali.node.memory.total"], int64(snap.MemTotal))
		o.ObserveInt64(ints["skali.node.disk.used"], int64(snap.DiskUsed))
		o.ObserveInt64(ints["skali.node.disk.total"], int64(snap.DiskTotal))
		o.ObserveInt64(ints["skali.node.network.rx.rate"], int64(snap.NetRxRate))
		o.ObserveInt64(ints["skali.node.network.tx.rate"], int64(snap.NetTxRate))
		o.ObserveInt64(ints["skali.node.disk.io.read.rate"], int64(snap.DiskReadRate))
		o.ObserveInt64(ints["skali.node.disk.io.write.rate"], int64(snap.DiskWriteRate))
		return nil
	}, observables...)
	return err
}
