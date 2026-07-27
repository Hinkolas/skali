package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

const (
	HostdBinaryPath        = "/usr/local/libexec/skali-hostd"
	HostdAgentUnit         = "skali-node-agent.service"
	HostdCoordinatorUnit   = "skali-coordinator.service"
	HostdAgentUnitPath     = "/etc/systemd/system/" + HostdAgentUnit
	HostdCoordinatorPath   = "/etc/systemd/system/" + HostdCoordinatorUnit
	AgentStateDir          = StateDir + "/agent"
	AgentConfigPath        = AgentStateDir + "/config.yaml"
	AgentCACertPath        = AgentStateDir + "/ca.crt"
	AgentClientCertPath    = AgentStateDir + "/client.crt"
	AgentClientKeyPath     = AgentStateDir + "/client.key"
	AgentPendingCSRPath    = AgentStateDir + "/enrollment.csr"
	AgentPendingKeyPath    = AgentStateDir + "/enrollment.key"
	CoordinatorAdminSocket = "/run/skali/coordinator.sock"
)

type AgentConfig struct {
	Version        int      `yaml:"version"`
	Cluster        string   `yaml:"cluster"`
	NodeID         string   `yaml:"nodeId"`
	NodeName       string   `yaml:"nodeName"`
	InstallationID string   `yaml:"installationId"`
	Endpoints      []string `yaml:"endpoints"`
	CACert         string   `yaml:"caCert"`
	ClientCert     string   `yaml:"clientCert"`
	ClientKey      string   `yaml:"clientKey"`
	PollSeconds    int      `yaml:"pollSeconds"`
}

type EnrollmentRecordOptions struct {
	Cluster        string
	NodeID         string
	InstallationID string
	NodeName       string
	Role           string
	Capabilities   []string
	Network        NodeNetwork
	Endpoint       string
	CAPin          string
	AgentVersion   string
}

// PrepareEnrollmentRecord claims only Skali's local state. It is called
// after authenticated remote preflight and before the invitation is consumed;
// no k3s path or service is touched.
func PrepareEnrollmentRecord(ctx context.Context, runner host.Runner, opts EnrollmentRecordOptions) (*Record, error) {
	if opts.Cluster == "" || opts.NodeID == "" || opts.InstallationID == "" ||
		opts.NodeName == "" || opts.Endpoint == "" || opts.CAPin == "" {
		return nil, errors.New("enrollment record is missing identity or coordinator trust")
	}
	if opts.Role != layout.RoleAgent && opts.Role != layout.RoleServer {
		return nil, fmt.Errorf("enrollment role must be agent or server")
	}
	if len(opts.Capabilities) == 0 {
		return nil, errors.New("enrollment requires at least one capability")
	}
	for _, capability := range opts.Capabilities {
		if !slices.Contains(layout.Capabilities, capability) {
			return nil, fmt.Errorf("unknown capability %q", capability)
		}
	}
	detected, err := Detect(ctx, runner)
	if err != nil {
		return nil, err
	}
	if (detected.State == StateEnrolled || detected.State == StateInterrupted) &&
		detected.Record != nil && detected.Record.Reconciled() &&
		!detected.Record.RegistrationMayHaveStarted() {
		record := detected.Record
		if record.InstallationID == opts.InstallationID && record.Node.ID == opts.NodeID &&
			record.Cluster == opts.Cluster {
			return record, nil
		}
		return nil, errors.New("this host is already enrolled with a different identity")
	}
	if detected.State != StateFresh {
		return nil, fmt.Errorf("enrollment requires a fresh host, found state %s", detected.State)
	}
	now := time.Now().UTC().Truncate(time.Second)
	record := &Record{
		Version: RecordVersionReconciled, InstallationID: opts.InstallationID,
		Provider: ProviderK3s, Cluster: opts.Cluster, Ownership: OwnershipManaged,
		Management: ManagementReconciled,
		Node:       newEnrollmentNodeRecord(opts),
		Join:       &JoinRecord{Server: opts.Endpoint},
		Coordinator: &CoordinatorRecord{
			Endpoints: []string{opts.Endpoint}, CAPin: opts.CAPin,
			AgentVersion: opts.AgentVersion,
		},
		Versions: Versions{Installer: opts.AgentVersion, K3s: K3sVersion},
		Lifecycle: &InstallLifecycle{
			Status: InstallStatusEnrolled, Phase: InstallPhaseEnrolled,
			StartedAt: now, UpdatedAt: now,
		},
		CreatedAt: now,
	}
	if err := SaveRecord(ctx, runner, record); err != nil {
		return nil, err
	}
	return record, nil
}

// newEnrollmentNodeRecord carries the operator's address declaration into
// the enrollment record, where the coordinator-driven install reads it
// back: the typed action only knows the cluster address.
func newEnrollmentNodeRecord(opts EnrollmentRecordOptions) NodeRecord {
	node := NodeRecord{
		ID: opts.NodeID, Name: opts.NodeName, Role: opts.Role,
		Capabilities: append([]string(nil), opts.Capabilities...),
	}
	node.SetNetwork(opts.Network)
	return node
}

func StageHostd(ctx context.Context, runner host.Runner, binary []byte, coordinator bool) error {
	if len(binary) == 0 {
		return errors.New("skali-hostd binary is empty")
	}
	if err := runner.MkdirAll(ctx, "/usr/local/libexec", 0o755); err != nil {
		return fmt.Errorf("create hostd binary directory: %w", err)
	}
	if err := runner.ReplaceFile(ctx, HostdBinaryPath, "", binary, 0o755); err != nil {
		return fmt.Errorf("install %s: %w", HostdBinaryPath, err)
	}
	if err := runner.ReplaceFile(ctx, HostdAgentUnitPath, "",
		[]byte(hostdUnit("agent")), 0o644); err != nil {
		return fmt.Errorf("install %s: %w", HostdAgentUnit, err)
	}
	if coordinator {
		if err := runner.ReplaceFile(ctx, HostdCoordinatorPath, "",
			[]byte(hostdUnit("coordinator")), 0o644); err != nil {
			return fmt.Errorf("install %s: %w", HostdCoordinatorUnit, err)
		}
	}
	result, err := runner.Run(ctx, host.Command{
		Name: "systemctl", Args: []string{"daemon-reload"},
	})
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("reload systemd after hostd install: %s",
			strings.TrimSpace(result.Stderr))
	}
	return nil
}

func hostdUnit(mode string) string {
	description := "Skali node lifecycle agent"
	after := "network-online.target"
	if mode == "coordinator" {
		description = "Skali cluster coordinator"
		after += " k3s.service"
	}
	return `[Unit]
Description=` + description + `
After=` + after + `
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + HostdBinaryPath + ` ` + mode + `
Restart=always
RestartSec=3s
UMask=0077
PrivateTmp=true
ProtectHome=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`
}

func SaveAgentIdentity(ctx context.Context, runner host.Runner, config AgentConfig,
	caCert, clientCert, clientKey []byte) error {
	if config.Version == 0 {
		config.Version = 1
	}
	if config.PollSeconds <= 0 {
		config.PollSeconds = 5
	}
	config.CACert = AgentCACertPath
	config.ClientCert = AgentClientCertPath
	config.ClientKey = AgentClientKeyPath
	if err := runner.MkdirAll(ctx, AgentStateDir, 0o700); err != nil {
		return err
	}
	for path, data := range map[string][]byte{
		AgentCACertPath: caCert, AgentClientCertPath: clientCert,
		AgentClientKeyPath: clientKey,
	} {
		if len(data) == 0 {
			return fmt.Errorf("agent identity file %s is empty", path)
		}
		if err := runner.ReplaceFile(ctx, path, "", data, 0o600); err != nil {
			return err
		}
	}
	data, err := encodeYAML(config)
	if err != nil {
		return err
	}
	if err := runner.ReplaceFile(ctx, AgentConfigPath, "", data, 0o600); err != nil {
		return err
	}
	if err := runner.Remove(ctx, AgentPendingCSRPath); err != nil {
		return err
	}
	return runner.Remove(ctx, AgentPendingKeyPath)
}

// UpdateAgentEndpoints atomically persists coordinator discovery without
// rewriting client credentials. The coordinator advertises only active
// servers, so retaining this list gives every previously enrolled node
// automatic failover after topology convergence.
func UpdateAgentEndpoints(ctx context.Context, runner host.Runner, config AgentConfig,
	endpoints []string) error {
	if len(endpoints) == 0 {
		return errors.New("agent must retain at least one coordinator endpoint")
	}
	normalized := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		value, err := clusterstate.NormalizeEndpoint(endpoint)
		if err != nil {
			return err
		}
		if !slices.Contains(normalized, value) {
			normalized = append(normalized, value)
		}
	}
	config.Endpoints = normalized
	data, err := encodeYAML(config)
	if err != nil {
		return err
	}
	return runner.ReplaceFile(ctx, AgentConfigPath, AgentConfigPath+".prev", data, 0o600)
}

// CacheCoordinatorState keeps the last non-secret operation references in
// the atomic local record. Status and repair can therefore explain an
// interrupted apply even while this host cannot reach Kubernetes.
func CacheCoordinatorState(ctx context.Context, runner host.Runner,
	response clusterstate.AgentPollResponse) error {
	record, err := LoadRecord(ctx, runner)
	if err != nil || record.Coordinator == nil {
		return err
	}
	current := record.Coordinator
	if current.ConvergedRevision == response.ConvergedRevision &&
		current.TargetRevision == response.TargetRevision &&
		current.CandidateRevision == response.CandidateRevision &&
		current.LastOperation == response.OperationID &&
		current.LastOperationPhase == response.OperationPhase {
		return nil
	}
	current.ConvergedRevision = response.ConvergedRevision
	current.TargetRevision = response.TargetRevision
	current.CandidateRevision = response.CandidateRevision
	current.LastOperation = response.OperationID
	current.LastOperationPhase = response.OperationPhase
	return SaveRecord(ctx, runner, record)
}

// LoadAgentConfig reads the non-secret routing part of the local mTLS
// identity through the host abstraction, so CLI recovery also works in Lima.
func LoadAgentConfig(ctx context.Context, runner host.Runner) (AgentConfig, error) {
	data, err := runner.ReadFile(ctx, AgentConfigPath)
	if err != nil {
		return AgentConfig{}, err
	}
	var config AgentConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return AgentConfig{}, fmt.Errorf("parse %s: %w", AgentConfigPath, err)
	}
	if config.NodeID == "" || config.InstallationID == "" || len(config.Endpoints) == 0 {
		return AgentConfig{}, errors.New("agent config is missing identity or coordinator endpoints")
	}
	return config, nil
}

// ResumeEnrolledHostd repairs the post-enrollment/start window without
// redeeming the one-use invitation again. SaveAgentIdentity removes the
// pending CSR only after every identity file is durable, so the presence of
// this complete identity proves the coordinator already accepted it.
func ResumeEnrolledHostd(ctx context.Context, runner host.Runner, record *Record,
	binary []byte, preferredEndpoint string) (bool, error) {
	config, err := LoadAgentConfig(ctx, runner)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, path := range []string{config.CACert, config.ClientCert, config.ClientKey} {
		info, statErr := runner.Stat(ctx, path)
		if statErr != nil {
			return false, statErr
		}
		if !info.Exists {
			return false, nil
		}
	}
	if record == nil || config.NodeID != record.Node.ID ||
		config.InstallationID != record.InstallationID ||
		config.Cluster != record.Cluster {
		return false, errors.New("agent identity does not match the installation record")
	}
	endpoints := preferAgentEndpoint(config.Endpoints, preferredEndpoint)
	if err := UpdateAgentEndpoints(ctx, runner, config, endpoints); err != nil {
		return false, err
	}
	if err := StageHostd(ctx, runner, binary, record.Node.Role == layout.RoleServer); err != nil {
		return false, err
	}
	// A staged server is not a coordinator until cluster apply has joined
	// k3s; only the outbound agent starts during enrollment recovery.
	if err := StartHostd(ctx, runner, false); err != nil {
		return false, err
	}
	record.Coordinator.Endpoints = endpoints
	record.Lifecycle.Status = InstallStatusEnrolled
	record.Lifecycle.Phase = InstallPhaseAwaitingApply
	record.Lifecycle.LastError = ""
	record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	if err := SaveRecord(ctx, runner, record); err != nil {
		return false, err
	}
	return true, nil
}

func preferAgentEndpoint(endpoints []string, preferred string) []string {
	values := append([]string(nil), endpoints...)
	if preferred != "" {
		values = append([]string{preferred}, values...)
	}
	result := values[:0]
	for _, endpoint := range values {
		normalized, err := clusterstate.NormalizeEndpoint(endpoint)
		if err == nil && !slices.Contains(result, normalized) {
			result = append(result, normalized)
		}
	}
	return result
}

// EnrollmentCSR persists the pre-certificate key and CSR so a network
// interruption can redeem the same one-use invitation idempotently.
func EnrollmentCSR(ctx context.Context, runner host.Runner,
	nodeID, nodeName string) (privateKey, csr []byte, err error) {
	privateKey, keyErr := runner.ReadFile(ctx, AgentPendingKeyPath)
	csr, csrErr := runner.ReadFile(ctx, AgentPendingCSRPath)
	if keyErr == nil && csrErr == nil && len(privateKey) > 0 && len(csr) > 0 {
		return privateKey, csr, nil
	}
	if keyErr != nil && !errors.Is(keyErr, fs.ErrNotExist) {
		return nil, nil, keyErr
	}
	if csrErr != nil && !errors.Is(csrErr, fs.ErrNotExist) {
		return nil, nil, csrErr
	}
	privateKey, csr, err = clusterstate.NewAgentKeyAndCSR(nodeID, nodeName)
	if err != nil {
		return nil, nil, err
	}
	if err := runner.MkdirAll(ctx, AgentStateDir, 0o700); err != nil {
		return nil, nil, err
	}
	if err := runner.ReplaceFile(ctx, AgentPendingKeyPath, "", privateKey, 0o600); err != nil {
		return nil, nil, err
	}
	if err := runner.ReplaceFile(ctx, AgentPendingCSRPath, "", csr, 0o600); err != nil {
		return nil, nil, err
	}
	return privateKey, csr, nil
}

func StartHostd(ctx context.Context, runner host.Runner, coordinator bool) error {
	units := []string{HostdAgentUnit}
	if coordinator {
		units = append(units, HostdCoordinatorUnit)
	}
	for _, unit := range units {
		result, err := runner.Run(ctx, host.Command{
			Name: "systemctl", Args: []string{"enable", "--now", unit},
		})
		if err != nil {
			return fmt.Errorf("start %s: %w", unit, err)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("start %s: exit %d: %s", unit, result.ExitCode,
				strings.TrimSpace(result.Stderr))
		}
		result, err = runner.Run(ctx, host.Command{
			Name: "systemctl", Args: []string{"is-active", unit},
		})
		if err != nil {
			return fmt.Errorf("verify %s: %w", unit, err)
		}
		if result.ExitCode != 0 {
			detail := strings.TrimSpace(result.Stderr)
			if detail == "" {
				detail = strings.TrimSpace(result.Stdout)
			}
			return fmt.Errorf("%s did not stay active: %s", unit, detail)
		}
	}
	return nil
}

func StopHostd(ctx context.Context, runner host.Runner) error {
	result, err := runner.Run(ctx, host.Command{
		Name: "systemctl", Args: []string{"stop", HostdCoordinatorUnit, HostdAgentUnit},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("stop host services: exit %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func RemoveHostd(ctx context.Context, runner host.Runner, removeBinary bool) error {
	for _, unit := range []string{HostdCoordinatorUnit, HostdAgentUnit} {
		_, _ = runner.Run(ctx, host.Command{Name: "systemctl", Args: []string{"disable", "--now", unit}})
	}
	for _, path := range []string{HostdCoordinatorPath, HostdAgentUnitPath} {
		if err := runner.Remove(ctx, path); err != nil {
			return err
		}
	}
	if removeBinary {
		if err := runner.Remove(ctx, HostdBinaryPath); err != nil {
			return err
		}
	}
	_, _ = runner.Run(ctx, host.Command{Name: "systemctl", Args: []string{"daemon-reload"}})
	return nil
}

// HostdPresent lets status and repair distinguish an enrolled record from a
// manually damaged one.
func HostdPresent(ctx context.Context, runner host.Runner) bool {
	info, err := runner.Stat(ctx, HostdBinaryPath)
	return err == nil && info.Exists && info.Mode&fs.ModeType == 0
}

func encodeYAML(value any) ([]byte, error) {
	return yaml.Marshal(value)
}
