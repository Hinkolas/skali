package installer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/version"
)

// HostState classifies what the installer found on this host. Detection is
// pure read; no state ever mutates anything.
type HostState string

const (
	// StateFresh is a supported host with no k3s and no record.
	StateFresh HostState = "fresh"
	// StateServer and StateAgent are skali-managed k3s hosts whose record
	// matches the installed role. An inactive service is a health detail,
	// not a different state.
	StateServer HostState = "server"
	StateAgent  HostState = "agent"
	// StateEnrolled is a version-2 host authenticated with the coordinator
	// but deliberately awaiting cluster apply before k3s is installed.
	StateEnrolled HostState = "enrolled"
	// StateUnmanaged is a host running k3s without a skali record. The
	// adoption guard applies: it is never adopted or destroyed.
	StateUnmanaged HostState = "unmanaged"
	// StateOrphaned is a recordless k3s host carrying the complete,
	// Skali-specific config fingerprint of an older interrupted install.
	// It is recoverable only after explicit operator confirmation.
	StateOrphaned HostState = "orphaned"
	// StateInterrupted is a Skali-owned installation transaction that did
	// not reach its complete phase.
	StateInterrupted HostState = "interrupted"
	// StateDamaged has a record that is unreadable or contradicts host
	// state; Problems lists what was found.
	StateDamaged HostState = "damaged"
	// StateUnsupported cannot host an installation (not linux, no
	// systemd, not root).
	StateUnsupported HostState = "unsupported"
)

// Host carries every fact detection gathered, enough to render the status
// header without further probes.
type Host struct {
	State    HostState
	Hostname string
	// OS is the pretty name from os-release; Arch is the machine
	// architecture.
	OS   string
	Arch string
	// K3sVersion is the installed k3s version, empty when absent.
	K3sVersion string
	// K3sActive reports whether the role-matching k3s unit is active.
	K3sActive bool
	// Record is the loaded installation record; nil unless state is
	// server, agent, or damaged with a readable record.
	Record *Record
	// RecordRecovered means Record came from RecordBackupPath because the
	// primary record was missing or unreadable.
	RecordRecovered bool
	// Problems explains damaged and unsupported states.
	Problems []string
}

// Detect classifies this host from the record and host probes, performing
// zero writes.
func Detect(ctx context.Context, runner host.Runner) (*Host, error) {
	detected := &Host{}
	detected.Hostname = probeLine(ctx, runner, "hostname")
	detected.Arch = probeLine(ctx, runner, "uname", "-m")
	detected.OS = probeOSRelease(ctx, runner)

	if unsupported := probeUnsupported(ctx, runner); len(unsupported) > 0 {
		detected.State = StateUnsupported
		detected.Problems = unsupported
		return detected, nil
	}

	binary, err := runner.Stat(ctx, K3sBinaryPath)
	if err != nil {
		return nil, fmt.Errorf("probe %s: %w", K3sBinaryPath, err)
	}
	serverUnit := probeUnit(ctx, runner, "k3s.service")
	agentUnit := probeUnit(ctx, runner, "k3s-agent.service")
	k3sPresent := binary.Exists || serverUnit.present || agentUnit.present
	if binary.Exists {
		detected.K3sVersion = probeK3sVersion(ctx, runner)
	}

	record, recordErr := LoadRecord(ctx, runner)
	switch {
	case errors.Is(recordErr, ErrNoRecord):
		if k3sPresent {
			if orphan := detectOrphanRecord(ctx, runner, serverUnit, agentUnit); orphan != nil {
				detected.State = StateOrphaned
				detected.Record = orphan
				detected.Problems = []string{
					"k3s has Skali-owned node labels and registry configuration, but the installation record is missing",
				}
				return detected, nil
			}
			detected.State = StateUnmanaged
			return detected, nil
		}
		detected.State = StateFresh
		return detected, nil
	case recordErr != nil:
		detected.State = StateDamaged
		detected.Problems = append(detected.Problems, recordErr.Error())
		return detected, nil
	}
	detected.Record = record
	detected.RecordRecovered = recordRecovered(ctx, runner)

	if record.EnrolledOnly() && !k3sPresent {
		if HostdPresent(ctx, runner) {
			detected.State = StateEnrolled
		} else {
			detected.State = StateInterrupted
			detected.Problems = append(detected.Problems,
				"the reconciled enrollment record exists but "+HostdBinaryPath+" is missing")
		}
		return detected, nil
	}

	var problems []string
	if detected.RecordRecovered {
		problems = append(problems, "the primary installation record is missing or unreadable; using "+
			RecordBackupPath)
	}
	if !binary.Exists {
		problems = append(problems, "installation record exists but "+K3sBinaryPath+" is missing")
	}
	switch record.Node.Role {
	case layout.RoleServer:
		if !serverUnit.present && binary.Exists {
			problems = append(problems, "record says server but the k3s.service unit is missing")
		}
		detected.K3sActive = serverUnit.active
	case layout.RoleAgent:
		if !agentUnit.present && binary.Exists {
			problems = append(problems, "record says agent but the k3s-agent.service unit is missing")
		}
		detected.K3sActive = agentUnit.active
	default:
		problems = append(problems, fmt.Sprintf("record has unknown node role %q", record.Node.Role))
	}
	if record.Reconciled() {
		if !HostdPresent(ctx, runner) {
			problems = append(problems, "reconciled installation is missing "+HostdBinaryPath)
		}
		if !probeUnit(ctx, runner, HostdAgentUnit).present {
			problems = append(problems, "reconciled installation is missing "+HostdAgentUnit)
		}
		if record.Node.Role == layout.RoleServer &&
			!probeUnit(ctx, runner, HostdCoordinatorUnit).present {
			problems = append(problems,
				"reconciled server is missing "+HostdCoordinatorUnit)
		}
	}

	if !record.InstallComplete() {
		detected.State = StateInterrupted
		detected.Problems = problems
		return detected, nil
	}
	if len(problems) > 0 {
		detected.State = StateDamaged
		detected.Problems = problems
		return detected, nil
	}
	if record.Node.Role == layout.RoleAgent {
		detected.State = StateAgent
	} else {
		detected.State = StateServer
	}
	return detected, nil
}

type orphanK3sConfig struct {
	NodeName   string   `yaml:"node-name"`
	Server     string   `yaml:"server"`
	NodeLabels []string `yaml:"node-label"`
}

// detectOrphanRecord recognizes only the pair of Skali-owned fingerprints
// written by installK3s: cluster/capability labels plus the managed
// registry mirror. A binary, unit, state directory, or cache file alone is
// never enough to cross the unmanaged-host boundary.
func detectOrphanRecord(ctx context.Context, runner host.Runner, serverUnit, agentUnit unitStatus) *Record {
	configData, err := runner.ReadFile(ctx, K3sConfigPath)
	if err != nil {
		return nil
	}
	registriesData, err := runner.ReadFile(ctx, K3sRegistriesPath)
	if err != nil {
		return nil
	}
	var registries struct {
		Mirrors map[string]any `yaml:"mirrors"`
	}
	if err := yaml.Unmarshal(registriesData, &registries); err != nil {
		return nil
	}
	if _, managedRegistry := registries.Mirrors[bundle.RegistryInternalHost]; !managedRegistry {
		return nil
	}
	var config orphanK3sConfig
	if err := yaml.Unmarshal(configData, &config); err != nil {
		return nil
	}
	labels := map[string]string{}
	for _, entry := range config.NodeLabels {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			labels[key] = value
		}
	}
	cluster := labels[layout.ClusterLabel]
	capabilities := layout.CapabilitiesFromLabels(labels)
	if cluster == "" || config.NodeName == "" || len(capabilities) == 0 {
		return nil
	}
	if serverUnit.present && agentUnit.present {
		return nil
	}
	role := layout.RoleAgent
	if serverUnit.present {
		role = layout.RoleServer
	} else if !agentUnit.present {
		return nil
	}
	record := &Record{
		Version:   RecordVersion,
		Provider:  ProviderK3s,
		Cluster:   cluster,
		Ownership: OwnershipManaged,
		Node: NodeRecord{
			Name:         config.NodeName,
			Role:         role,
			Capabilities: capabilities,
		},
		Versions: Versions{Installer: version.Version, K3s: K3sVersion},
	}
	if config.Server != "" {
		record.Join = &JoinRecord{Server: config.Server}
	}
	return record
}

// PersistOrphanRecord converts a read-only orphan reconstruction into a
// durable interrupted transaction after the command layer has obtained
// explicit consent.
func PersistOrphanRecord(ctx context.Context, runner host.Runner, record *Record) error {
	if record == nil || record.Cluster == "" || record.Node.Role == "" || len(record.Node.Capabilities) == 0 {
		return errors.New("cannot recover orphaned install: required Skali fingerprints are incomplete")
	}
	recovered := *record
	recovered.InstallationID = uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	recovered.Lifecycle = &InstallLifecycle{
		Status:         InstallStatusFailed,
		Phase:          InstallPhaseStarting,
		AttemptID:      uuid.NewString(),
		StartAttempted: true,
		LastError:      "recovered ownership of an interrupted install created before transaction records",
		StartedAt:      now,
		UpdatedAt:      now,
	}
	if err := SaveRecord(ctx, runner, &recovered); err != nil {
		return err
	}
	*record = recovered
	return nil
}

// probeUnsupported returns the failed platform requirements.
func probeUnsupported(ctx context.Context, runner host.Runner) []string {
	var problems []string
	if kernel := probeLine(ctx, runner, "uname", "-s"); kernel != "" && kernel != "Linux" {
		problems = append(problems, "skali cluster requires Linux, this host runs "+kernel)
	}
	systemd, err := runner.Stat(ctx, "/run/systemd/system")
	if err == nil && !systemd.Exists {
		problems = append(problems, "skali cluster requires systemd")
	}
	if uid := probeLine(ctx, runner, "id", "-u"); uid != "" && uid != "0" {
		problems = append(problems, "skali cluster must run as root; re-run it under sudo")
	}
	return problems
}

type unitStatus struct {
	present bool
	active  bool
}

// probeUnit asks systemd about one unit. is-enabled distinguishes a unit
// that exists from one systemd has never seen; is-active reports health.
func probeUnit(ctx context.Context, runner host.Runner, unit string) unitStatus {
	status := unitStatus{}
	enabled, err := runner.Run(ctx, host.Command{Name: "systemctl", Args: []string{"is-enabled", unit}})
	if err == nil {
		state := strings.TrimSpace(enabled.Stdout)
		status.present = enabled.ExitCode == 0 || (state != "" && state != "not-found")
	}
	active, err := runner.Run(ctx, host.Command{Name: "systemctl", Args: []string{"is-active", unit}})
	if err == nil && active.ExitCode == 0 {
		status.present = true
		status.active = true
	}
	return status
}

func probeK3sVersion(ctx context.Context, runner host.Runner) string {
	result, err := runner.Run(ctx, host.Command{Name: "k3s", Args: []string{"--version"}})
	if err != nil || result.ExitCode != 0 {
		return ""
	}
	// First line: "k3s version v1.33.3+k3s1 (hash)".
	line, _, _ := strings.Cut(result.Stdout, "\n")
	for field := range strings.FieldsSeq(line) {
		if strings.HasPrefix(field, "v") && strings.Contains(field, "k3s") {
			return field
		}
	}
	return strings.TrimSpace(line)
}

func probeOSRelease(ctx context.Context, runner host.Runner) string {
	data, err := runner.ReadFile(ctx, "/etc/os-release")
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

func probeLine(ctx context.Context, runner host.Runner, name string, args ...string) string {
	result, err := runner.Run(ctx, host.Command{Name: name, Args: args})
	if err != nil || result.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}
