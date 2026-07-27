package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"gopkg.in/yaml.v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
)

// ErrNoRecord reports an absent installation record.
var ErrNoRecord = errors.New("no installation record")

// RecordVersion remains the legacy imperative schema. New managed clusters
// use RecordVersionReconciled and are routed through staged enrollment.
const (
	RecordVersion           = "1"
	RecordVersionReconciled = "2"

	ManagementLegacy     = "legacy"
	ManagementReconciled = "reconciled"
)

// Record is the root-owned installation record at RecordPath (section
// 14.1). It identifies the installation and carries versions and
// bookkeeping, never secrets: the auth secret and admin password live only
// in cluster Secrets. Loading is deliberately lenient about unknown fields
// so an older installer can still report status of a newer installation;
// the operator-authored configs are the strict surfaces.
type Record struct {
	Version        string `yaml:"version"`
	InstallationID string `yaml:"installationId"`
	Provider       string `yaml:"provider"`
	Cluster        string `yaml:"cluster"`
	Ownership      string `yaml:"ownership"`
	// Management distinguishes existing imperative installations from the
	// coordinator/agent lifecycle. Version-1 records omit it and are
	// treated as legacy.
	Management string     `yaml:"management,omitempty"`
	Node       NodeRecord `yaml:"node"`
	// Join keeps which server this agent enrolled against; servers leave
	// it nil.
	Join *JoinRecord `yaml:"join,omitempty"`
	// Endpoints and TLS are gathered at install or init time; both stay
	// empty until known.
	Endpoints *Endpoints `yaml:"endpoints,omitempty"`
	TLS       *TLSConfig `yaml:"tls,omitempty"`
	// Existing carries the shape of an existing-cluster installation (a
	// cluster whose hosts skali does not administer); nil for managed
	// installations. It is a bundle-hash input, so its choices correctly
	// move the hash.
	Existing *ExistingClusterRecord `yaml:"existing,omitempty"`
	// RegistryNode pins the installer-owned local registry volume to the
	// hostname selected by the first reconciled initialization. Legacy
	// records omit it and retain capability-only scheduling.
	RegistryNode string   `yaml:"registryNode,omitempty"`
	Versions     Versions `yaml:"versions"`
	// Lifecycle is present while a managed host install is in progress or
	// failed. Records written before lifecycle tracking omit it and are
	// treated as complete.
	Lifecycle   *InstallLifecycle  `yaml:"lifecycle,omitempty"`
	Coordinator *CoordinatorRecord `yaml:"coordinator,omitempty"`
	CreatedAt   time.Time          `yaml:"createdAt"`
	UpdatedAt   time.Time          `yaml:"updatedAt"`
}

const (
	InstallStatusInstalling = "installing"
	InstallStatusEnrolled   = "enrolled"
	InstallStatusRemoving   = "removing"
	InstallStatusFailed     = "failed"
	InstallStatusComplete   = "complete"

	InstallPhasePrepared      = "prepared"
	InstallPhaseEnrolled      = "enrolled"
	InstallPhaseAwaitingApply = "awaiting-apply"
	InstallPhaseConfigured    = "configured"
	InstallPhaseInstalled     = "installed"
	InstallPhaseStarting      = "starting"
	InstallPhaseJoined        = "joined"
	InstallPhaseActive        = "active"
	InstallPhaseDraining      = "draining"
	InstallPhaseUninstalling  = "uninstalling"
	InstallPhaseRemoved       = "removed"
	InstallPhaseComplete      = "complete"
)

// InstallLifecycle is the durable transaction journal embedded in the
// ownership record. It deliberately contains no join or registry secrets.
type InstallLifecycle struct {
	Status         string    `yaml:"status"`
	Phase          string    `yaml:"phase"`
	AttemptID      string    `yaml:"attemptId,omitempty"`
	StartAttempted bool      `yaml:"startAttempted,omitempty"`
	LastError      string    `yaml:"lastError,omitempty"`
	LastLog        string    `yaml:"lastLog,omitempty"`
	StartedAt      time.Time `yaml:"startedAt,omitempty"`
	UpdatedAt      time.Time `yaml:"updatedAt,omitempty"`
}

// InstallComplete treats legacy records with no lifecycle as complete.
func (r *Record) InstallComplete() bool {
	return r != nil && (r.Lifecycle == nil ||
		r.Lifecycle.Status == InstallStatusComplete ||
		r.Lifecycle.Phase == InstallPhaseComplete)
}

func (r *Record) Reconciled() bool {
	return r != nil && (r.Version == RecordVersionReconciled ||
		r.Management == ManagementReconciled)
}

func (r *Record) EnrolledOnly() bool {
	return r != nil && r.Reconciled() && r.Lifecycle != nil &&
		(r.Lifecycle.Phase == InstallPhaseEnrolled ||
			r.Lifecycle.Phase == InstallPhaseAwaitingApply) &&
		!r.Lifecycle.StartAttempted
}

// RegistrationMayHaveStarted is deliberately conservative for legacy
// records: before lifecycle tracking, every completed install had started
// k3s.
func (r *Record) RegistrationMayHaveStarted() bool {
	return r != nil && (r.Lifecycle == nil || r.Lifecycle.StartAttempted)
}

// ExistingClusterRecord holds the operator's declared shape for an
// existing-cluster installation: the fields the bundle needs that a
// managed installation instead derives from node labels and host files.
type ExistingClusterRecord struct {
	IngressClassName string          `yaml:"ingressClassName"`
	StorageClassName string          `yaml:"storageClassName,omitempty"`
	DatabaseTier     string          `yaml:"databaseTier"`
	DatabaseStorage  string          `yaml:"databaseStorage"`
	RegistryStorage  string          `yaml:"registryStorage"`
	Capabilities     []string        `yaml:"capabilities"`
	Operators        OperatorsRecord `yaml:"operators"`
}

// OperatorsRecord records whether the vendored operators were installed
// or an existing installation was reused, so a bundle uninstall never
// deletes an operator namespace skali did not create.
type OperatorsRecord struct {
	CNPG        string `yaml:"cnpg"`
	CertManager string `yaml:"certManager"`
}

// JoinRecord is the enrollment bookkeeping of an agent node.
type JoinRecord struct {
	Server string `yaml:"server"`
}

// NodeRecord identifies this host within the installation. IP is the
// cluster address: the one other nodes reach this node through, and the
// one the coordinator endpoint and the k3s node-ip are built from. The
// remaining address fields are omitted on records written before the node
// network was declarable, which keeps the canonical record byte-identical
// for those installations.
type NodeRecord struct {
	ID        string   `yaml:"id,omitempty"`
	Name      string   `yaml:"name"`
	IP        string   `yaml:"ip,omitempty"`
	PublicIPs []string `yaml:"publicIPs,omitempty"`
	ExtraSANs []string `yaml:"extraSANs,omitempty"`
	// CoordinatorBind lists the scopes the enrollment coordinator listens
	// on; empty means the cluster address only.
	CoordinatorBind []string `yaml:"coordinatorBind,omitempty"`
	Role            string   `yaml:"role"`
	Capabilities    []string `yaml:"capabilities"`
}

// Network reassembles the address declaration recorded for this node.
func (n NodeRecord) Network() NodeNetwork {
	return NodeNetwork{
		ClusterIP:       n.IP,
		PublicIPs:       append([]string(nil), n.PublicIPs...),
		ExtraSANs:       append([]string(nil), n.ExtraSANs...),
		CoordinatorBind: append([]string(nil), n.CoordinatorBind...),
	}
}

// SetNetwork records an address declaration, leaving fields the caller did
// not resolve untouched.
func (n *NodeRecord) SetNetwork(network NodeNetwork) {
	network = network.Normalize()
	if network.ClusterIP != "" {
		n.IP = network.ClusterIP
	}
	n.PublicIPs = network.PublicIPs
	n.ExtraSANs = network.ExtraSANs
	n.CoordinatorBind = network.CoordinatorBind
}

// CoordinatorRecord contains only non-secret enrollment routing and trust
// metadata. The agent key and certificate live in separate root-owned files.
type CoordinatorRecord struct {
	Endpoints          []string `yaml:"endpoints,omitempty"`
	CAPin              string   `yaml:"caPin,omitempty"`
	AgentVersion       string   `yaml:"agentVersion,omitempty"`
	ConvergedRevision  string   `yaml:"convergedRevision,omitempty"`
	TargetRevision     string   `yaml:"targetRevision,omitempty"`
	CandidateRevision  string   `yaml:"candidateRevision,omitempty"`
	LastOperation      string   `yaml:"lastOperation,omitempty"`
	LastOperationPhase string   `yaml:"lastOperationPhase,omitempty"`
}

// Endpoints are the public domains of the installation.
type Endpoints struct {
	API string `yaml:"api"`
	// Registry is the public managed-registry domain; empty on records
	// written before initialization gathered it.
	Registry string `yaml:"registry,omitempty"`
}

// TLSConfig parameterizes certificate issuance.
type TLSConfig struct {
	IssuerEmail string `yaml:"issuerEmail"`
	// ACMEServer overrides the ACME directory URL; empty selects the
	// Let's Encrypt production endpoint.
	ACMEServer string `yaml:"acmeServer,omitempty"`
}

// Versions records what is installed. Bundle is set by init; empty means
// the cluster was never initialized from this host. The bundle hash is
// deliberately NOT recorded here: the record text itself is a bundle input
// (it is published in-cluster), so carrying the hash would move the hash.
// The namespace annotation stamped by the converge is the sole marker.
type Versions struct {
	Installer string `yaml:"installer"`
	K3s       string `yaml:"k3s"`
	Bundle    string `yaml:"bundle,omitempty"`
}

// LoadRecord reads the root-owned record through the runner.
func LoadRecord(ctx context.Context, runner host.Runner) (*Record, error) {
	data, err := runner.ReadFile(ctx, RecordPath)
	if errors.Is(err, fs.ErrNotExist) {
		backup, backupErr := runner.ReadFile(ctx, RecordBackupPath)
		if errors.Is(backupErr, fs.ErrNotExist) {
			return nil, ErrNoRecord
		}
		if backupErr != nil {
			return nil, fmt.Errorf("read installation record backup: %w", backupErr)
		}
		record, parseErr := parseRecord(backup, RecordBackupPath)
		if parseErr != nil {
			return nil, fmt.Errorf("primary installation record is missing and backup is unusable: %w", parseErr)
		}
		return record, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read installation record: %w", err)
	}
	record, parseErr := parseRecord(data, RecordPath)
	if parseErr == nil {
		return record, nil
	}
	backup, backupErr := runner.ReadFile(ctx, RecordBackupPath)
	if backupErr == nil {
		if recovered, recoveredErr := parseRecord(backup, RecordBackupPath); recoveredErr == nil {
			return recovered, nil
		}
	}
	return nil, parseErr
}

func parseRecord(data []byte, path string) (*Record, error) {
	var record Record
	if err := yaml.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("parse installation record %s: %w", path, err)
	}
	if record.Version == "" || record.InstallationID == "" {
		return nil, fmt.Errorf("installation record %s is missing its identity fields", path)
	}
	return &record, nil
}

// recordRecovered reports whether LoadRecord would need the backup because
// the primary is absent or invalid.
func recordRecovered(ctx context.Context, runner host.Runner) bool {
	data, err := runner.ReadFile(ctx, RecordPath)
	if err != nil {
		return true
	}
	_, err = parseRecord(data, RecordPath)
	return err != nil
}

// InClusterRecord reads the installation record Init published as a
// ConfigMap. It identifies the init owner: the node whose canonical record
// is a bundle-hash input, and therefore the only node whose init/upgrade
// may converge the bundle. A missing ConfigMap returns (nil, nil): the
// cluster was never initialized.
func InClusterRecord(ctx context.Context, client *kube.Client) (*Record, error) {
	configMap, err := client.Clientset.CoreV1().ConfigMaps(bundle.Namespace).
		Get(ctx, bundle.RecordName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read in-cluster installation record: %w", err)
	}
	var record Record
	if err := yaml.Unmarshal([]byte(configMap.Data[bundle.RecordKey]), &record); err != nil {
		return nil, fmt.Errorf("parse in-cluster installation record: %w", err)
	}
	if record.InstallationID == "" {
		return nil, fmt.Errorf("in-cluster installation record is missing its identity fields")
	}
	return &record, nil
}

// SaveRecord writes the record root-owned: directory 0750, file 0600. The
// record holds no secrets, but it is an authority artifact nothing outside
// root needs.
func SaveRecord(ctx context.Context, runner host.Runner, record *Record) error {
	record.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	if record.CreatedAt.IsZero() {
		record.CreatedAt = record.UpdatedAt
	}
	data, err := yaml.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode installation record: %w", err)
	}
	if err := runner.MkdirAll(ctx, StateDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", StateDir, err)
	}
	backup := ""
	if current, err := runner.ReadFile(ctx, RecordPath); err == nil {
		if _, err := parseRecord(current, RecordPath); err == nil {
			backup = RecordBackupPath
		}
	}
	if err := runner.ReplaceFile(ctx, RecordPath, backup, data, 0o600); err != nil {
		return fmt.Errorf("write installation record: %w", err)
	}
	return nil
}

// RemoveRecord deletes the record; scoped uninstall removes it last so an
// interrupted uninstall re-detects as damaged rather than fresh.
func RemoveRecord(ctx context.Context, runner host.Runner) error {
	if err := runner.Remove(ctx, RecordPath); err != nil {
		return err
	}
	return runner.Remove(ctx, RecordBackupPath)
}

// CanonicalYAML renders the record for the in-cluster copy and therefore
// for the bundle hash: volatile fields (timestamps) are omitted so a
// repeat run over unchanged inputs reproduces the exact bytes, and with
// them the stamped hash.
func (r *Record) CanonicalYAML() (string, error) {
	type canonicalRecord struct {
		Version        string                 `yaml:"version"`
		InstallationID string                 `yaml:"installationId"`
		Provider       string                 `yaml:"provider"`
		Cluster        string                 `yaml:"cluster"`
		Ownership      string                 `yaml:"ownership"`
		Management     string                 `yaml:"management,omitempty"`
		Node           NodeRecord             `yaml:"node"`
		Join           *JoinRecord            `yaml:"join,omitempty"`
		Endpoints      *Endpoints             `yaml:"endpoints,omitempty"`
		TLS            *TLSConfig             `yaml:"tls,omitempty"`
		Existing       *ExistingClusterRecord `yaml:"existing,omitempty"`
		RegistryNode   string                 `yaml:"registryNode,omitempty"`
		Versions       Versions               `yaml:"versions"`
	}
	data, err := yaml.Marshal(canonicalRecord{
		Version:        r.Version,
		InstallationID: r.InstallationID,
		Provider:       r.Provider,
		Cluster:        r.Cluster,
		Ownership:      r.Ownership,
		Management:     r.Management,
		Node:           r.Node,
		Join:           r.Join,
		Endpoints:      r.Endpoints,
		TLS:            r.TLS,
		Existing:       r.Existing,
		RegistryNode:   r.RegistryNode,
		Versions:       r.Versions,
	})
	if err != nil {
		return "", fmt.Errorf("encode canonical installation record: %w", err)
	}
	return string(data), nil
}
