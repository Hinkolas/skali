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

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/manifest"
)

// NodeConfig is the per-host install configuration (node.yaml) consumed by
// `skali cluster install --config`. Non-interactive runs take every
// decision from it and fail rather than prompt.
type NodeConfig struct {
	// Cluster names the installation; the first server creates it.
	// Defaults to production.
	Cluster string `yaml:"cluster,omitempty" json:"cluster,omitempty" jsonschema:"Cluster name created by the first server. Defaults to production."`
	// Role is the K3s role of this host: server or agent.
	Role string `yaml:"role,omitempty" json:"role,omitempty" jsonschema:"K3s role: server or agent. Current join tokens supply it for joining nodes."`
	// Capabilities designates what this node runs.
	Capabilities []string `yaml:"capabilities" json:"capabilities" jsonschema:"Designated workload capabilities for this node."`
	// NodeIP pins the address this node advertises inside the cluster.
	// Superseded by network.clusterIP, which it is an alias for.
	NodeIP string `yaml:"nodeIP,omitempty" json:"nodeIP,omitempty" jsonschema:"Deprecated alias for network.clusterIP."`
	// Network declares how this host is addressed. On a multi-homed host
	// (a cloud server on a private network plus a public interface) the
	// default-route address is the public one, so leaving this out puts
	// cluster traffic, the coordinator listener, and the API certificate
	// on the public interface.
	Network *NetworkConfig `yaml:"network,omitempty" json:"network,omitempty" jsonschema:"How this host is addressed: cluster address, public addresses, and certificate names."`
	// Join enrolls this host into an existing cluster, as an agent or as
	// an additional server; required for agents, absent for the first
	// server.
	Join *JoinConfig `yaml:"join,omitempty" json:"join,omitempty" jsonschema:"Join an existing cluster instead of creating one."`
	// VM shapes the Lima VM that hosts this node on a macOS machine.
	// Linux installs refuse it.
	VM *VMConfig `yaml:"vm,omitempty" json:"vm,omitempty" jsonschema:"macOS only: the Lima VM hosting this node."`
}

// NetworkConfig declares the addresses of one host and what each is for.
// A cloud server with a private network interface and a public one
// declares both, so cluster traffic and enrollment stay private while the
// API certificate still covers the public address.
type NetworkConfig struct {
	ClusterIP       string   `yaml:"clusterIP,omitempty" json:"clusterIP,omitempty" jsonschema:"Address other cluster nodes reach this node through. Defaults to the address of the default route."`
	PublicIPs       []string `yaml:"publicIPs,omitempty" json:"publicIPs,omitempty" jsonschema:"Addresses reachable from outside the cluster network. Floating or NAT-mapped addresses may be declared here without being assigned to the host."`
	ExtraSANs       []string `yaml:"extraSANs,omitempty" json:"extraSANs,omitempty" jsonschema:"Additional names or addresses to place in the Kubernetes API server certificate, such as a load balancer or a DNS name used in kubeconfigs."`
	CoordinatorBind []string `yaml:"coordinatorBind,omitempty" json:"coordinatorBind,omitempty" jsonschema:"Scopes the enrollment coordinator listens on: cluster, public, or both. Defaults to cluster."`
}

// NodeNetwork returns the address declaration, folding the deprecated
// nodeIP alias into the cluster address.
func (c *NodeConfig) NodeNetwork() NodeNetwork {
	network := NodeNetwork{ClusterIP: c.NodeIP}
	if c.Network != nil {
		if c.Network.ClusterIP != "" {
			network.ClusterIP = c.Network.ClusterIP
		}
		network.PublicIPs = append([]string(nil), c.Network.PublicIPs...)
		network.ExtraSANs = append([]string(nil), c.Network.ExtraSANs...)
		network.CoordinatorBind = append([]string(nil), c.Network.CoordinatorBind...)
	}
	return network.Normalize()
}

// JoinConfig points a joining host at an existing server.
type JoinConfig struct {
	Server    string `yaml:"server,omitempty" json:"server,omitempty" jsonschema:"Optional alternate URL of an existing K3s server; current Skali tokens supply a default."`
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
	Endpoints EndpointsConfig      `yaml:"endpoints" json:"endpoints"`
	TLS       TLSInitConfig        `yaml:"tls" json:"tls"`
	Admin     AdminConfig          `yaml:"admin" json:"admin"`
	Skalid    SkalidConfig         `yaml:"skalid" json:"skalid"`
	Web       WebConfig            `yaml:"web" json:"web"`
	Storage   *StorageInitConfig   `yaml:"storage,omitempty" json:"storage,omitempty"`
	Platforms *PlatformsInitConfig `yaml:"platforms,omitempty" json:"platforms,omitempty"`
}

// StorageDriver folds the optional storage block: empty keeps the
// recorded driver.
func (c *InitConfig) StorageDriver() string {
	if c.Storage != nil {
		return c.Storage.Driver
	}
	return ""
}

// PlatformPreference folds the optional platforms block: empty keeps the
// recorded preference.
func (c *InitConfig) PlatformPreference() []string {
	if c.Platforms != nil {
		return c.Platforms.Preference
	}
	return nil
}

// PlatformsInitConfig configures build platform selection on
// mixed-architecture clusters.
type PlatformsInitConfig struct {
	// Preference is an ordered list of platforms (linux/amd64,
	// linux/arm64): the first entry an application supports wins its
	// single-arch build. Empty keeps the recorded preference, multi-arch
	// builds on a fresh installation.
	Preference []string `yaml:"preference,omitempty" json:"preference,omitempty" jsonschema:"Ordered build platform preference for mixed-architecture clusters: the first entry an application supports wins its single-arch build. Entries are linux/amd64 or linux/arm64. Empty keeps the recorded choice, multi-arch builds on a fresh installation."`
}

// StorageInitConfig selects the application storage layer.
type StorageInitConfig struct {
	// Driver is local or longhorn. Empty keeps the cluster's recorded
	// driver, local on a fresh installation.
	Driver string `yaml:"driver,omitempty" json:"driver,omitempty" jsonschema:"Application storage driver: local (default, k3s local-path, no replication or size enforcement) or longhorn (replicated block storage with enforced volume sizes). Empty keeps the recorded choice."`
}

// EndpointsConfig declares the public domains.
type EndpointsConfig struct {
	API      string `yaml:"api" json:"api" jsonschema:"Public api/ui domain, for example skali.example.com."`
	Registry string `yaml:"registry" json:"registry" jsonschema:"Public managed-registry domain, for example cr.skali.example.com."`
	S3       string `yaml:"s3,omitempty" json:"s3,omitempty" jsonschema:"Optional public S3 endpoint domain, for example s3.skali.example.com; empty keeps bucket access in-cluster."`
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

// WebConfig selects the web console image serving the platform domain
// root; the daemon answers behind /api on the same domain.
type WebConfig struct {
	Image string `yaml:"image" json:"image" jsonschema:"skali-web console image reference to install."`
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
	if config.Cluster == "" && config.Join == nil {
		config.Cluster = DefaultCluster
	}
	if config.Role == "" && config.Join == nil {
		return nil, errors.New("node config: role is required when creating the first server")
	}
	if config.Role != "" && config.Role != layout.RoleServer && config.Role != layout.RoleAgent {
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
	if config.NodeIP != "" && config.Network != nil && config.Network.ClusterIP != "" &&
		config.Network.ClusterIP != config.NodeIP {
		return nil, fmt.Errorf("node config: nodeIP %q and network.clusterIP %q disagree; "+
			"nodeIP is the deprecated alias, keep only network.clusterIP",
			config.NodeIP, config.Network.ClusterIP)
	}
	if err := config.NodeNetwork().Validate(); err != nil {
		return nil, fmt.Errorf("node config: network: %w", err)
	}
	if config.Role == layout.RoleAgent && config.Join == nil {
		return nil, errors.New("node config: role agent requires a join block pointing at an existing server")
	}
	if config.Join != nil {
		if config.Join.Server != "" {
			if _, err := normalizeJoinServer(config.Join.Server); err != nil {
				return nil, fmt.Errorf("node config: %w", err)
			}
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
	if config.Web.Image == "" {
		return nil, errors.New("init config: web.image is required")
	}
	if config.Storage != nil {
		switch config.Storage.Driver {
		case "", bundle.StorageDriverLocal, bundle.StorageDriverLonghorn:
		default:
			return nil, fmt.Errorf("init config: storage.driver must be %s or %s, got %q",
				bundle.StorageDriverLocal, bundle.StorageDriverLonghorn, config.Storage.Driver)
		}
	}
	if config.Platforms != nil {
		if err := validatePlatformPreference(config.Platforms.Preference); err != nil {
			return nil, fmt.Errorf("init config: platforms.preference %w", err)
		}
	}
	return &config, nil
}

// validatePlatformPreference checks an ordered platform preference list
// against the platforms skali can build for.
func validatePlatformPreference(preference []string) error {
	seen := make(map[string]struct{}, len(preference))
	for _, platform := range preference {
		if !slices.Contains(manifest.KnownPlatforms, platform) {
			return fmt.Errorf("must list only %s, got %q",
				strings.Join(manifest.KnownPlatforms, " or "), platform)
		}
		if _, ok := seen[platform]; ok {
			return fmt.Errorf("lists %q twice", platform)
		}
		seen[platform] = struct{}{}
	}
	return nil
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
