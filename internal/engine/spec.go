package engine

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

// specConfigs translates a ContainerSpec into the daemon's create payload.
// Pure — unit-tested without a daemon. Validation here covers what would
// otherwise surface as an opaque daemon error; semantic validation (name
// rules, kind, label namespace) is the master's job.
func specConfigs(spec ContainerSpec) (*container.Config, *container.HostConfig, *network.NetworkingConfig, error) {
	if spec.Image == "" {
		return nil, nil, nil, fmt.Errorf("engine: spec has no image")
	}

	cfg := &container.Config{
		Image:      spec.Image,
		Cmd:        spec.Command,
		Entrypoint: spec.Entrypoint,
		Env:        envSlice(spec.Env),
		Labels:     stampedLabels(spec.Labels),
	}
	if hc := spec.Healthcheck; hc != nil {
		cfg.Healthcheck = &container.HealthConfig{
			Test:        hc.Test,
			Interval:    hc.Interval,
			Timeout:     hc.Timeout,
			StartPeriod: hc.StartPeriod,
			Retries:     hc.Retries,
		}
	}

	host := &container.HostConfig{}
	host.NanoCPUs = spec.NanoCPUs
	host.Memory = spec.MemoryLimit

	restart, err := restartPolicy(spec)
	if err != nil {
		return nil, nil, nil, err
	}
	host.RestartPolicy = restart

	for _, m := range spec.Mounts {
		var typ mount.Type
		switch m.Type {
		case "bind":
			typ = mount.TypeBind
		case "volume":
			typ = mount.TypeVolume
		default:
			return nil, nil, nil, fmt.Errorf("engine: invalid mount type %q (bind or volume)", m.Type)
		}
		if m.Source == "" || m.Target == "" {
			return nil, nil, nil, fmt.Errorf("engine: mount needs both source and target")
		}
		host.Mounts = append(host.Mounts, mount.Mount{
			Type: typ, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly,
		})
	}

	exposed, bindings, err := portConfig(spec.Ports)
	if err != nil {
		return nil, nil, nil, err
	}
	cfg.ExposedPorts = exposed
	host.PortBindings = bindings

	var netCfg *network.NetworkingConfig
	if len(spec.Networks) > 0 {
		netCfg = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
		for _, name := range spec.Networks {
			netCfg.EndpointsConfig[name] = &network.EndpointSettings{}
		}
	}

	return cfg, host, netCfg, nil
}

// stampedLabels copies the spec labels and force-stamps ownership: whatever
// the caller sent, everything the engine creates is skali-managed.
func stampedLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	maps.Copy(out, labels)
	out[LabelManaged] = "true"
	return out
}

// envSlice renders the env map as the daemon's KEY=VALUE list, sorted so the
// payload is deterministic.
func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}

func restartPolicy(spec ContainerSpec) (container.RestartPolicy, error) {
	if spec.RestartMaxRetries != 0 && spec.Restart != RestartOnFailure {
		return container.RestartPolicy{}, fmt.Errorf("engine: restart max retries requires the on-failure policy")
	}
	switch spec.Restart {
	case "", RestartNone:
		return container.RestartPolicy{}, nil
	case RestartAlways, RestartUnlessStopped:
		return container.RestartPolicy{Name: container.RestartPolicyMode(spec.Restart)}, nil
	case RestartOnFailure:
		return container.RestartPolicy{
			Name:              container.RestartPolicyOnFailure,
			MaximumRetryCount: spec.RestartMaxRetries,
		}, nil
	default:
		return container.RestartPolicy{}, fmt.Errorf("engine: invalid restart policy %q", spec.Restart)
	}
}

func portConfig(ports []PortBinding) (network.PortSet, network.PortMap, error) {
	if len(ports) == 0 {
		return nil, nil, nil
	}
	exposed := network.PortSet{}
	bindings := network.PortMap{}
	for _, pb := range ports {
		proto := pb.Protocol
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" && proto != "udp" {
			return nil, nil, fmt.Errorf("engine: invalid port protocol %q (tcp or udp)", pb.Protocol)
		}
		port, ok := network.PortFrom(pb.ContainerPort, network.IPProtocol(proto))
		if !ok || pb.ContainerPort == 0 {
			return nil, nil, fmt.Errorf("engine: invalid container port %d", pb.ContainerPort)
		}
		binding := network.PortBinding{}
		if pb.HostPort != 0 {
			binding.HostPort = strconv.Itoa(int(pb.HostPort))
		}
		if pb.HostIP != "" {
			addr, err := netip.ParseAddr(pb.HostIP)
			if err != nil {
				return nil, nil, fmt.Errorf("engine: invalid host IP %q", pb.HostIP)
			}
			binding.HostIP = addr
		}
		exposed[port] = struct{}{}
		bindings[port] = append(bindings[port], binding)
	}
	return exposed, bindings, nil
}
