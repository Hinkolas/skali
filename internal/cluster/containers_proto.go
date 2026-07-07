package cluster

import (
	"time"

	"github.com/Hinkolas/skali/internal/clusterpb"
	"github.com/Hinkolas/skali/internal/engine"
)

// Conversions between engine types and their wire form — the single home for
// both directions, shared by the worker's NodeService, the master's remote
// handle, and the poller's self-stamp path. Engine types are the currency
// everywhere; proto exists only on the wire.

func specToProto(s engine.ContainerSpec) *clusterpb.ContainerSpec {
	p := &clusterpb.ContainerSpec{
		Name:              s.Name,
		Image:             s.Image,
		Env:               s.Env,
		Command:           s.Command,
		Entrypoint:        s.Entrypoint,
		RestartPolicy:     restartToProto(s.Restart),
		RestartMaxRetries: uint32(s.RestartMaxRetries),
		NanoCpus:          s.NanoCPUs,
		MemoryLimitBytes:  s.MemoryLimit,
		Networks:          s.Networks,
		Labels:            s.Labels,
	}
	for _, m := range s.Mounts {
		p.Mounts = append(p.Mounts, &clusterpb.ContainerMount{
			Type: m.Type, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly,
		})
	}
	for _, pb := range s.Ports {
		p.Ports = append(p.Ports, &clusterpb.ContainerPort{
			HostIp:        pb.HostIP,
			HostPort:      uint32(pb.HostPort),
			ContainerPort: uint32(pb.ContainerPort),
			Protocol:      pb.Protocol,
		})
	}
	if hc := s.Healthcheck; hc != nil {
		p.Healthcheck = &clusterpb.Healthcheck{
			Test:          hc.Test,
			IntervalMs:    hc.Interval.Milliseconds(),
			TimeoutMs:     hc.Timeout.Milliseconds(),
			Retries:       uint32(hc.Retries),
			StartPeriodMs: hc.StartPeriod.Milliseconds(),
		}
	}
	return p
}

func specFromProto(p *clusterpb.ContainerSpec) engine.ContainerSpec {
	s := engine.ContainerSpec{
		Name:              p.GetName(),
		Image:             p.GetImage(),
		Env:               p.GetEnv(),
		Command:           p.GetCommand(),
		Entrypoint:        p.GetEntrypoint(),
		Restart:           restartFromProto(p.GetRestartPolicy()),
		RestartMaxRetries: int(p.GetRestartMaxRetries()),
		NanoCPUs:          p.GetNanoCpus(),
		MemoryLimit:       p.GetMemoryLimitBytes(),
		Networks:          p.GetNetworks(),
		Labels:            p.GetLabels(),
	}
	for _, m := range p.GetMounts() {
		s.Mounts = append(s.Mounts, engine.Mount{
			Type: m.GetType(), Source: m.GetSource(), Target: m.GetTarget(), ReadOnly: m.GetReadOnly(),
		})
	}
	for _, pb := range p.GetPorts() {
		s.Ports = append(s.Ports, engine.PortBinding{
			HostIP:        pb.GetHostIp(),
			HostPort:      uint16(pb.GetHostPort()),
			ContainerPort: uint16(pb.GetContainerPort()),
			Protocol:      pb.GetProtocol(),
		})
	}
	if hc := p.GetHealthcheck(); hc != nil {
		s.Healthcheck = &engine.Healthcheck{
			Test:        hc.GetTest(),
			Interval:    time.Duration(hc.GetIntervalMs()) * time.Millisecond,
			Timeout:     time.Duration(hc.GetTimeoutMs()) * time.Millisecond,
			StartPeriod: time.Duration(hc.GetStartPeriodMs()) * time.Millisecond,
			Retries:     int(hc.GetRetries()),
		}
	}
	return s
}

func restartToProto(r engine.RestartPolicy) clusterpb.RestartPolicy {
	switch r {
	case engine.RestartAlways:
		return clusterpb.RestartPolicy_RESTART_POLICY_ALWAYS
	case engine.RestartUnlessStopped:
		return clusterpb.RestartPolicy_RESTART_POLICY_UNLESS_STOPPED
	case engine.RestartOnFailure:
		return clusterpb.RestartPolicy_RESTART_POLICY_ON_FAILURE
	default: // "" and "no"
		return clusterpb.RestartPolicy_RESTART_POLICY_NO
	}
}

func restartFromProto(r clusterpb.RestartPolicy) engine.RestartPolicy {
	switch r {
	case clusterpb.RestartPolicy_RESTART_POLICY_ALWAYS:
		return engine.RestartAlways
	case clusterpb.RestartPolicy_RESTART_POLICY_UNLESS_STOPPED:
		return engine.RestartUnlessStopped
	case clusterpb.RestartPolicy_RESTART_POLICY_ON_FAILURE:
		return engine.RestartOnFailure
	default: // UNSPECIFIED and NO
		return engine.RestartNone
	}
}

func pullFromProto(p clusterpb.PullPolicy) engine.PullPolicy {
	switch p {
	case clusterpb.PullPolicy_PULL_POLICY_ALWAYS:
		return engine.PullAlways
	case clusterpb.PullPolicy_PULL_POLICY_NEVER:
		return engine.PullNever
	default: // UNSPECIFIED and IF_MISSING
		return engine.PullIfMissing
	}
}

func pullToProto(p engine.PullPolicy) clusterpb.PullPolicy {
	switch p {
	case engine.PullAlways:
		return clusterpb.PullPolicy_PULL_POLICY_ALWAYS
	case engine.PullNever:
		return clusterpb.PullPolicy_PULL_POLICY_NEVER
	default: // "" and if-missing
		return clusterpb.PullPolicy_PULL_POLICY_IF_MISSING
	}
}

// containerFromProto is containerInfoProto's inverse (stats are dropped —
// they live in observed state, not on the Container).
func containerFromProto(info *clusterpb.ContainerInfo) engine.Container {
	c := engine.Container{
		ID:           info.GetId(),
		Name:         info.GetName(),
		Image:        info.GetImage(),
		State:        info.GetState(),
		Health:       info.GetHealth(),
		ExitCode:     int(info.GetExitCode()),
		RestartCount: int(info.GetRestartCount()),
		Labels:       info.GetLabels(),
	}
	if ts := info.GetCreatedAtUnix(); ts != 0 {
		c.CreatedAt = time.Unix(ts, 0)
	}
	if ts := info.GetStartedAtUnix(); ts != 0 {
		c.StartedAt = time.Unix(ts, 0)
	}
	return c
}

func containerInfoProto(c engine.Container, stats *engine.Stats) *clusterpb.ContainerInfo {
	info := &clusterpb.ContainerInfo{
		Id:           c.ID,
		Name:         c.Name,
		Image:        c.Image,
		State:        c.State,
		Health:       c.Health,
		ExitCode:     int32(c.ExitCode),
		Labels:       c.Labels,
		RestartCount: uint32(c.RestartCount),
	}
	if !c.CreatedAt.IsZero() {
		info.CreatedAtUnix = c.CreatedAt.Unix()
	}
	if !c.StartedAt.IsZero() {
		info.StartedAtUnix = c.StartedAt.Unix()
	}
	if stats != nil {
		info.Stats = &clusterpb.ContainerStats{
			CpuPercent:       stats.CPUPercent,
			MemoryUsedBytes:  stats.MemUsed,
			MemoryLimitBytes: stats.MemLimit,
			NetRxBytesPerSec: stats.NetRxRate,
			NetTxBytesPerSec: stats.NetTxRate,
		}
	}
	return info
}

// containerReport renders the sampler's observations for the wire; nil when
// the engine state is unknown (absent on the wire = unknown, never empty).
func containerReport(sampler *engine.Sampler) *clusterpb.ContainerReport {
	obs, ok := sampler.Latest()
	if !ok {
		return nil
	}
	report := &clusterpb.ContainerReport{Containers: make([]*clusterpb.ContainerInfo, 0, len(obs))}
	for _, o := range obs {
		report.Containers = append(report.Containers, containerInfoProto(o.Container, o.Stats))
	}
	return report
}
