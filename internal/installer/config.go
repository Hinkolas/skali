package installer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/layout"
)

// NodeConfig is the per-host install configuration (node.yaml) consumed by
// `skali cluster install --config`. Non-interactive runs take every
// decision from it and fail rather than prompt.
type NodeConfig struct {
	// Cluster names the installation; the first server creates it.
	// Defaults to production.
	Cluster string `yaml:"cluster,omitempty" json:"cluster,omitempty" jsonschema:"Cluster name created by the first server. Defaults to production."`
	// Role is the K3s role of this host: server or agent.
	Role string `yaml:"role" json:"role" jsonschema:"K3s role: server or agent."`
	// Capabilities designates what this node runs.
	Capabilities []string `yaml:"capabilities" json:"capabilities" jsonschema:"Designated workload capabilities for this node."`
	// NodeIP pins the address this node advertises inside the cluster.
	// Needed on multi-homed hosts where the default-route interface is not
	// the one other nodes can reach.
	NodeIP string `yaml:"nodeIP,omitempty" json:"nodeIP,omitempty" jsonschema:"Optional IP address this node advertises inside the cluster; set it on multi-homed hosts."`
	// Join enrolls this host into an existing cluster, as an agent or as
	// an additional server; required for agents, absent for the first
	// server.
	Join *JoinConfig `yaml:"join,omitempty" json:"join,omitempty" jsonschema:"Join an existing cluster instead of creating one."`
	// VM shapes the Lima VM that hosts this node on a macOS machine.
	// Linux installs refuse it.
	VM *VMConfig `yaml:"vm,omitempty" json:"vm,omitempty" jsonschema:"macOS only: the Lima VM hosting this node."`
}

// JoinConfig points a joining host at an existing server.
type JoinConfig struct {
	Server    string `yaml:"server" json:"server" jsonschema:"URL of an existing K3s server, for example https://cp-1.internal:6443."`
	TokenFile string `yaml:"tokenFile" json:"tokenFile" jsonschema:"Path to a file holding the join token."`
}

// VMConfig shapes the Lima VM that hosts a node on macOS, where Kubernetes
// nodes are Linux-only. Every field is optional; the defaults size the VM
// for a dedicated fleet Mac.
type VMConfig struct {
	Name    string `yaml:"name,omitempty" json:"name,omitempty" jsonschema:"Lima instance name. Defaults to skali."`
	Network string `yaml:"network,omitempty" json:"network,omitempty" jsonschema:"VM network mode: bridged, shared, or user-v2. Defaults to bridged."`
	CPUs    int    `yaml:"cpus,omitempty" json:"cpus,omitempty" jsonschema:"CPU cores allocated to the VM. Defaults to all host cores minus one."`
	Memory  string `yaml:"memory,omitempty" json:"memory,omitempty" jsonschema:"VM memory such as 12GiB. Defaults to host memory minus 4GiB."`
	Disk    string `yaml:"disk,omitempty" json:"disk,omitempty" jsonschema:"VM disk size such as 100GiB. Defaults to 100GiB."`
}

// InitConfig is the cluster initialization configuration (init.yaml)
// consumed by `skali cluster init --config`.
type InitConfig struct {
	Endpoints EndpointsConfig `yaml:"endpoints" json:"endpoints"`
	TLS       TLSInitConfig   `yaml:"tls" json:"tls"`
	Admin     AdminConfig     `yaml:"admin" json:"admin"`
	Skalid    SkalidConfig    `yaml:"skalid" json:"skalid"`
}

// EndpointsConfig declares the public domains.
type EndpointsConfig struct {
	API      string `yaml:"api" json:"api" jsonschema:"Public api/ui domain, for example skali.example.com."`
	Registry string `yaml:"registry" json:"registry" jsonschema:"Public managed-registry domain, for example registry.example.com."`
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
	if config.NodeIP != "" && net.ParseIP(config.NodeIP) == nil {
		return nil, fmt.Errorf("node config: nodeIP %q is not a valid IP address", config.NodeIP)
	}
	if config.Role == layout.RoleAgent && config.Join == nil {
		return nil, errors.New("node config: role agent requires a join block pointing at an existing server")
	}
	if config.Join != nil {
		if config.Join.Server == "" {
			return nil, errors.New("node config: join.server is required")
		}
		if !strings.HasPrefix(config.Join.Server, "https://") {
			return nil, fmt.Errorf("node config: join.server must be an https:// URL, got %q", config.Join.Server)
		}
		if config.Join.TokenFile == "" {
			return nil, errors.New("node config: join.tokenFile is required")
		}
	}
	if config.VM != nil {
		if err := config.VM.Validate(); err != nil {
			return nil, fmt.Errorf("node config: %w", err)
		}
	}
	return &config, nil
}

var (
	vmSizePattern = regexp.MustCompile(`^[1-9][0-9]*(GiB|MiB)$`)
	vmNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
)

// Validate checks shape only; whether a vm block is allowed at all is a
// platform decision the command layer makes.
func (vm *VMConfig) Validate() error {
	switch vm.Network {
	case "", "bridged", "shared", "user-v2":
	default:
		return fmt.Errorf("vm.network must be bridged, shared, or user-v2, got %q", vm.Network)
	}
	if vm.CPUs < 0 {
		return errors.New("vm.cpus must be a positive count")
	}
	if vm.Memory != "" && !vmSizePattern.MatchString(vm.Memory) {
		return fmt.Errorf("vm.memory %q is not a size like 12GiB", vm.Memory)
	}
	if vm.Disk != "" && !vmSizePattern.MatchString(vm.Disk) {
		return fmt.Errorf("vm.disk %q is not a size like 100GiB", vm.Disk)
	}
	if vm.Name != "" && !vmNamePattern.MatchString(vm.Name) {
		return fmt.Errorf("vm.name %q is not a valid Lima instance name", vm.Name)
	}
	return nil
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
	if config.Endpoints.Registry == "" {
		return nil, errors.New("init config: endpoints.registry is required")
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
