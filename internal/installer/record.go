package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/installer/host"
)

// ErrNoRecord reports an absent installation record.
var ErrNoRecord = errors.New("no installation record")

// RecordVersion is the current record schema version.
const RecordVersion = "1"

// Record is the root-owned installation record at RecordPath (section
// 14.1). It identifies the installation and carries versions and
// bookkeeping, never secrets: the auth secret and admin password live only
// in cluster Secrets. Loading is deliberately lenient about unknown fields
// so an older installer can still report status of a newer installation;
// the operator-authored configs are the strict surfaces.
type Record struct {
	Version        string     `yaml:"version"`
	InstallationID string     `yaml:"installationId"`
	Provider       string     `yaml:"provider"`
	Cluster        string     `yaml:"cluster"`
	Ownership      string     `yaml:"ownership"`
	Node           NodeRecord `yaml:"node"`
	// Join keeps which server this agent enrolled against; servers leave
	// it nil.
	Join *JoinRecord `yaml:"join,omitempty"`
	// Endpoints and TLS are gathered at install or init time; both stay
	// empty until known.
	Endpoints *Endpoints `yaml:"endpoints,omitempty"`
	TLS       *TLSConfig `yaml:"tls,omitempty"`
	Versions  Versions   `yaml:"versions"`
	CreatedAt time.Time  `yaml:"createdAt"`
	UpdatedAt time.Time  `yaml:"updatedAt"`
}

// JoinRecord is the enrollment bookkeeping of an agent node.
type JoinRecord struct {
	Server string `yaml:"server"`
}

// NodeRecord identifies this host within the installation.
type NodeRecord struct {
	Name         string   `yaml:"name"`
	Role         string   `yaml:"role"`
	Capabilities []string `yaml:"capabilities"`
}

// Endpoints are the public domains of the installation. The registry
// endpoint arrives with the public-registry slice; until then the managed
// registry is reachable in-cluster only.
type Endpoints struct {
	API string `yaml:"api"`
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
		return nil, ErrNoRecord
	}
	if err != nil {
		return nil, fmt.Errorf("read installation record: %w", err)
	}
	var record Record
	if err := yaml.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("parse installation record %s: %w", RecordPath, err)
	}
	if record.Version == "" || record.InstallationID == "" {
		return nil, fmt.Errorf("installation record %s is missing its identity fields", RecordPath)
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
	if err := runner.WriteFile(ctx, RecordPath, data, 0o600); err != nil {
		return fmt.Errorf("write installation record: %w", err)
	}
	return nil
}

// RemoveRecord deletes the record; scoped uninstall removes it last so an
// interrupted uninstall re-detects as damaged rather than fresh.
func RemoveRecord(ctx context.Context, runner host.Runner) error {
	return runner.Remove(ctx, RecordPath)
}

// CanonicalYAML renders the record for the in-cluster copy and therefore
// for the bundle hash: volatile fields (timestamps) are omitted so a
// repeat run over unchanged inputs reproduces the exact bytes, and with
// them the stamped hash.
func (r *Record) CanonicalYAML() (string, error) {
	type canonicalRecord struct {
		Version        string      `yaml:"version"`
		InstallationID string      `yaml:"installationId"`
		Provider       string      `yaml:"provider"`
		Cluster        string      `yaml:"cluster"`
		Ownership      string      `yaml:"ownership"`
		Node           NodeRecord  `yaml:"node"`
		Join           *JoinRecord `yaml:"join,omitempty"`
		Endpoints      *Endpoints  `yaml:"endpoints,omitempty"`
		TLS            *TLSConfig  `yaml:"tls,omitempty"`
		Versions       Versions    `yaml:"versions"`
	}
	data, err := yaml.Marshal(canonicalRecord{
		Version:        r.Version,
		InstallationID: r.InstallationID,
		Provider:       r.Provider,
		Cluster:        r.Cluster,
		Ownership:      r.Ownership,
		Node:           r.Node,
		Join:           r.Join,
		Endpoints:      r.Endpoints,
		TLS:            r.TLS,
		Versions:       r.Versions,
	})
	if err != nil {
		return "", fmt.Errorf("encode canonical installation record: %w", err)
	}
	return string(data), nil
}
