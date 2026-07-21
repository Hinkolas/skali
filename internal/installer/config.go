package installer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/layout"
)

// NodeConfig is the per-host install configuration (node.yaml) consumed by
// `skali-installer install --config`. Non-interactive runs take every
// decision from it and fail rather than prompt.
type NodeConfig struct {
	// Cluster names the installation; the first server creates it.
	// Defaults to production.
	Cluster string `yaml:"cluster,omitempty" json:"cluster,omitempty" jsonschema:"Cluster name created by the first server. Defaults to production."`
	// Role is the K3s role of this host: server or agent.
	Role string `yaml:"role" json:"role" jsonschema:"K3s role: server or agent."`
	// Capabilities designates what this node runs.
	Capabilities []string `yaml:"capabilities" json:"capabilities" jsonschema:"Designated workload capabilities for this node."`
	// Join enrolls this host into an existing cluster. Parsed and
	// schema-visible now; refused until the multi-node slice lands.
	Join *JoinConfig `yaml:"join,omitempty" json:"join,omitempty" jsonschema:"Join an existing cluster instead of creating one."`
}

// JoinConfig points a joining host at an existing server.
type JoinConfig struct {
	Server    string `yaml:"server" json:"server" jsonschema:"URL of an existing K3s server, for example https://cp-1.internal:6443."`
	TokenFile string `yaml:"tokenFile" json:"tokenFile" jsonschema:"Path to a file holding the join token."`
}

// InitConfig is the cluster initialization configuration (init.yaml)
// consumed by `skali-installer init --config`.
type InitConfig struct {
	Endpoints EndpointsConfig `yaml:"endpoints" json:"endpoints"`
	TLS       TLSInitConfig   `yaml:"tls" json:"tls"`
	Admin     AdminConfig     `yaml:"admin" json:"admin"`
	Skalid    SkalidConfig    `yaml:"skalid" json:"skalid"`
}

// EndpointsConfig declares the public domains. The registry domain arrives
// with the public-registry slice; the managed registry stays in-cluster
// only until then.
type EndpointsConfig struct {
	API string `yaml:"api" json:"api" jsonschema:"Public api/ui domain, for example skali.example.com."`
}

// TLSInitConfig parameterizes the ACME cluster issuer.
type TLSInitConfig struct {
	IssuerEmail string `yaml:"issuerEmail" json:"issuerEmail" jsonschema:"ACME account email for certificate issuance."`
	ACMEServer  string `yaml:"acmeServer,omitempty" json:"acmeServer,omitempty" jsonschema:"Optional ACME directory URL override; empty selects the Let's Encrypt production endpoint. Point test installations at the staging endpoint."`
}

// AdminConfig bootstraps the first operator account.
type AdminConfig struct {
	Email        string `yaml:"email" json:"email" jsonschema:"Email of the first admin account."`
	PasswordFile string `yaml:"passwordFile" json:"passwordFile" jsonschema:"Path to a file holding the admin password."`
}

// SkalidConfig selects the control-plane image. Required while no
// published bootstrap images exist.
type SkalidConfig struct {
	Image string `yaml:"image" json:"image" jsonschema:"skalid image reference to install."`
	// ImageID pins the content identity behind a mutable tag so a rebuilt
	// image rolls the deployment. Leave empty for immutable tags.
	ImageID string `yaml:"imageId,omitempty" json:"imageId,omitempty" jsonschema:"Optional image content identity behind a mutable tag."`
}

// ParseNodeConfig strictly parses one node.yaml document.
func ParseNodeConfig(data []byte) (*NodeConfig, error) {
	var config NodeConfig
	if err := strictParse(data, &config); err != nil {
		return nil, fmt.Errorf("node config: %w", err)
	}
	if config.Cluster == "" {
		config.Cluster = DefaultCluster
	}
	if config.Role != layout.RoleServer && config.Role != layout.RoleAgent {
		return nil, fmt.Errorf("node config: role must be server or agent, got %q", config.Role)
	}
	if len(config.Capabilities) == 0 {
		return nil, errors.New("node config: at least one capability is required")
	}
	for _, capability := range config.Capabilities {
		if !slices.Contains(layout.Capabilities, capability) {
			return nil, fmt.Errorf("node config: unknown capability %q; expected one of %s",
				capability, strings.Join(layout.Capabilities, ", "))
		}
	}
	if config.Join != nil {
		return nil, errors.New("node config: join is not implemented in this slice; multi-node enrollment arrives with a later milestone")
	}
	return &config, nil
}

// ParseInitConfig strictly parses one init.yaml document.
func ParseInitConfig(data []byte) (*InitConfig, error) {
	var config InitConfig
	if err := strictParse(data, &config); err != nil {
		return nil, fmt.Errorf("init config: %w", err)
	}
	if config.Endpoints.API == "" {
		return nil, errors.New("init config: endpoints.api is required")
	}
	if config.TLS.IssuerEmail == "" {
		return nil, errors.New("init config: tls.issuerEmail is required")
	}
	if config.Admin.Email == "" {
		return nil, errors.New("init config: admin.email is required")
	}
	if config.Admin.PasswordFile == "" {
		return nil, errors.New("init config: admin.passwordFile is required")
	}
	if config.Skalid.Image == "" {
		return nil, errors.New("init config: skalid.image is required")
	}
	return &config, nil
}

// strictParse decodes exactly one YAML document rejecting unknown fields,
// mirroring the layout parser's stance.
func strictParse(data []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple YAML documents are not supported")
		}
		return err
	}
	return nil
}
