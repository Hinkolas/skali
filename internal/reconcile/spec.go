package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/store"
)

// workloadNameRe also bounds container names: <name>-<ordinal> must fit the
// engine's rules, and lowercase keeps names usable as DNS labels later.
var workloadNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,53}$`)

// Spec is the user-declared container spec subset of a workload — what
// engine.ContainerSpec offers minus identity (the name derives from the
// workload), image (top-level, resolved through the mirror), and scheduling.
// Stored as JSONB on the workload row; this type is the single codec.
type Spec struct {
	Env               map[string]string `json:"env,omitempty"`
	Command           []string          `json:"command,omitempty"`
	Ports             []PortSpec        `json:"ports,omitempty"`
	Mounts            []MountSpec       `json:"mounts,omitempty"`
	RestartPolicy     string            `json:"restart_policy,omitempty"`
	RestartMaxRetries int               `json:"restart_max_retries,omitempty"`
	// CPUs limits a replica to a fraction of cores (0.5 = half a core);
	// 0 = unlimited.
	CPUs        float64           `json:"cpus,omitempty"`
	MemoryLimit int64             `json:"memory_limit,omitempty"`
	Networks    []string          `json:"networks,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Healthcheck *HealthcheckSpec  `json:"healthcheck,omitempty"`
}

type PortSpec struct {
	HostIP        string `json:"host_ip,omitempty"`
	HostPort      uint16 `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
	Protocol      string `json:"protocol,omitempty"`
}

type MountSpec struct {
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type HealthcheckSpec struct {
	Test               []string `json:"test"`
	IntervalSeconds    int      `json:"interval_seconds,omitempty"`
	TimeoutSeconds     int      `json:"timeout_seconds,omitempty"`
	StartPeriodSeconds int      `json:"start_period_seconds,omitempty"`
	Retries            int      `json:"retries,omitempty"`
}

// Constraints narrow where assignments may land; both filters intersect and
// empty means "any node". Placement itself stays reconciler-owned — users
// constrain, the reconciler assigns.
type Constraints struct {
	NodeIDs   []uuid.UUID `json:"node_ids,omitempty"`
	NodeRoles []string    `json:"node_roles,omitempty"`
}

// nodeRoles mirrors the nodes table's CHECK constraint.
var nodeRoles = []string{"master", "edge", "worker", "builder"}

func (s *Spec) validate() error {
	if err := engine.ValidateUserLabels(s.Labels); err != nil {
		return err
	}
	switch engine.RestartPolicy(s.RestartPolicy) {
	case "", engine.RestartNone, engine.RestartAlways, engine.RestartUnlessStopped, engine.RestartOnFailure:
	default:
		return fmt.Errorf("invalid restart_policy %q", s.RestartPolicy)
	}
	if s.RestartMaxRetries != 0 && engine.RestartPolicy(s.RestartPolicy) != engine.RestartOnFailure {
		return fmt.Errorf("restart_max_retries requires the on-failure policy")
	}
	if s.CPUs < 0 {
		return fmt.Errorf("cpus must not be negative")
	}
	if s.MemoryLimit < 0 {
		return fmt.Errorf("memory_limit must not be negative")
	}
	for _, p := range s.Ports {
		if p.ContainerPort == 0 {
			return fmt.Errorf("port mappings need a container_port")
		}
		switch p.Protocol {
		case "", "tcp", "udp":
		default:
			return fmt.Errorf("invalid port protocol %q", p.Protocol)
		}
	}
	for _, m := range s.Mounts {
		if m.Type != "bind" && m.Type != "volume" {
			return fmt.Errorf("invalid mount type %q (bind or volume)", m.Type)
		}
		if m.Source == "" || m.Target == "" {
			return fmt.Errorf("mounts need a source and a target")
		}
	}
	if s.Healthcheck != nil && len(s.Healthcheck.Test) == 0 {
		return fmt.Errorf("healthcheck needs a test command")
	}
	return nil
}

func (c *Constraints) validate() error {
	for _, r := range c.NodeRoles {
		valid := false
		for _, known := range nodeRoles {
			if r == known {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid node role %q", r)
		}
	}
	return nil
}

// workload is a decoded workloads row: the JSONB columns unmarshaled once.
type workload struct {
	store.Workload
	spec        Spec
	constraints Constraints
}

func decodeWorkload(row store.Workload) (*workload, error) {
	w := &workload{Workload: row}
	if err := json.Unmarshal(row.Spec, &w.spec); err != nil {
		return nil, fmt.Errorf("reconcile: decode spec of %s: %w", row.Name, err)
	}
	if err := json.Unmarshal(row.Constraints, &w.constraints); err != nil {
		return nil, fmt.Errorf("reconcile: decode constraints of %s: %w", row.Name, err)
	}
	return w, nil
}

func containerName(workloadName string, ordinal int32) string {
	return fmt.Sprintf("%s-%d", workloadName, ordinal)
}

// materialize compiles one assignment's engine spec, deterministically. The
// image is the digest-pinned mirror reference — every engine pull goes
// through the internal registry, and the pin survives catalog changes. The
// config-hash label fingerprints the whole materialized spec, so anything
// that must trigger a replace has to feed into it (which is also why a
// changed registry endpoint replaces containers — accepted).
func materialize(w *workload, ordinal int32, endpoint string) engine.ContainerSpec {
	spec := engine.ContainerSpec{
		Name:  containerName(w.Name, ordinal),
		Image: fmt.Sprintf("%s/%s@%s", endpoint, deref(w.ResolvedRepository), deref(w.ResolvedDigest)),
		Env:   w.spec.Env,
		// Restarts after crashes and reboots are docker's node-local
		// micro-goal; the reconciler only converges what docker cannot.
		Restart:           engine.RestartUnlessStopped,
		RestartMaxRetries: w.spec.RestartMaxRetries,
		Command:           w.spec.Command,
		NanoCPUs:          int64(w.spec.CPUs * 1e9),
		MemoryLimit:       w.spec.MemoryLimit,
		Networks:          w.spec.Networks,
	}
	if w.spec.RestartPolicy != "" {
		spec.Restart = engine.RestartPolicy(w.spec.RestartPolicy)
	}
	for _, p := range w.spec.Ports {
		spec.Ports = append(spec.Ports, engine.PortBinding{
			HostIP: p.HostIP, HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: p.Protocol,
		})
	}
	for _, m := range w.spec.Mounts {
		spec.Mounts = append(spec.Mounts, engine.Mount{
			Type: m.Type, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly,
		})
	}
	if h := w.spec.Healthcheck; h != nil {
		spec.Healthcheck = &engine.Healthcheck{
			Test:        h.Test,
			Interval:    time.Duration(h.IntervalSeconds) * time.Second,
			Timeout:     time.Duration(h.TimeoutSeconds) * time.Second,
			StartPeriod: time.Duration(h.StartPeriodSeconds) * time.Second,
			Retries:     h.Retries,
		}
	}

	labels := make(map[string]string, len(w.spec.Labels)+5)
	maps.Copy(labels, w.spec.Labels)
	labels[engine.LabelManaged] = "true"
	labels[engine.LabelKind] = w.Kind
	labels[engine.LabelWorkload] = w.ID.String()
	labels[engine.LabelInstance] = strconv.Itoa(int(ordinal))
	spec.Labels = labels
	// Hash over everything above; the hash label itself is excluded by
	// construction (stamped after).
	labels[engine.LabelConfigHash] = specHash(spec)
	return spec
}

// specHash fingerprints a materialized spec: sha256 over its canonical JSON
// (struct field order is fixed, map keys are sorted by encoding/json),
// truncated like the mirror's config hash.
func specHash(spec engine.ContainerSpec) string {
	raw, err := json.Marshal(spec)
	if err != nil { // unreachable for this struct; belt and braces
		return "unhashable"
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:6])
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
