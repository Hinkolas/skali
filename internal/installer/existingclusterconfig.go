package installer

import (
	"errors"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/Hinkolas/skali/internal/layout"
)

// ExistingClusterConfig is the configuration of an existing-cluster
// installation (`skali cluster install --mode existing-cluster --config`).
// It carries the fields a managed installation derives from node labels
// and host files but which no host access can supply here.
type ExistingClusterConfig struct {
	Cluster   string             `yaml:"cluster,omitempty" json:"cluster,omitempty" jsonschema:"Cluster name. Defaults to production."`
	Endpoints EndpointsConfig    `yaml:"endpoints" json:"endpoints"`
	TLS       TLSInitConfig      `yaml:"tls" json:"tls"`
	Admin     AdminConfig        `yaml:"admin" json:"admin"`
	Skalid    SkalidConfig       `yaml:"skalid" json:"skalid"`
	Ingress   IngressConfig      `yaml:"ingress" json:"ingress"`
	Storage   StorageConfig      `yaml:"storage,omitempty" json:"storage,omitempty"`
	Database  DatabaseConfig     `yaml:"database,omitempty" json:"database,omitempty"`
	Registry  RegistrySizeConfig `yaml:"registry,omitempty" json:"registry,omitempty"`
	Operators OperatorsConfig    `yaml:"operators,omitempty" json:"operators,omitempty"`
	// Capabilities the installation advertises; defaults to the required
	// set (application, database, registry, edge).
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty" jsonschema:"Capabilities the installation advertises; defaults to application, database, registry, edge."`
}

// IngressConfig names the cluster's ingress class; required because k3s's
// traefik default cannot be assumed on an existing cluster.
type IngressConfig struct {
	ClassName string `yaml:"className" json:"className" jsonschema:"IngressClass name for the api/ui and registry edges, for example nginx."`
}

// StorageConfig names the StorageClass for installer-owned volumes; empty
// relies on the cluster default.
type StorageConfig struct {
	ClassName string `yaml:"className,omitempty" json:"className,omitempty" jsonschema:"StorageClass for the database and registry volumes; empty uses the cluster default."`
}

// DatabaseConfig declares the bootstrap database shape. The tier is
// explicit here (no capability labels to count).
type DatabaseConfig struct {
	Tier    string `yaml:"tier,omitempty" json:"tier,omitempty" jsonschema:"Database availability tier: single, asynchronous, or synchronous. Defaults to single."`
	Storage string `yaml:"storage,omitempty" json:"storage,omitempty" jsonschema:"Bootstrap database volume size, for example 10Gi."`
}

// RegistrySizeConfig sizes the managed registry volume.
type RegistrySizeConfig struct {
	Storage string `yaml:"storage,omitempty" json:"storage,omitempty" jsonschema:"Managed registry volume size, for example 20Gi."`
}

// OperatorsConfig chooses whether to install the vendored operators or
// reuse operators the cluster already runs.
type OperatorsConfig struct {
	CNPG        string `yaml:"cnpg,omitempty" json:"cnpg,omitempty" jsonschema:"CNPG operator: install or use-existing. Defaults to install."`
	CertManager string `yaml:"certManager,omitempty" json:"certManager,omitempty" jsonschema:"cert-manager: install or use-existing. Defaults to install."`
}

// ParseExistingClusterConfig strictly parses one existing-cluster
// configuration document, defaulting the optional fields.
func ParseExistingClusterConfig(data []byte) (*ExistingClusterConfig, error) {
	var config ExistingClusterConfig
	if err := strictParse(data, &config); err != nil {
		return nil, fmt.Errorf("existing-cluster config: %w", err)
	}
	if config.Cluster == "" {
		config.Cluster = DefaultCluster
	}
	if config.Endpoints.API == "" {
		return nil, errors.New("existing-cluster config: endpoints.api is required")
	}
	if config.Endpoints.Registry == "" {
		return nil, errors.New("existing-cluster config: endpoints.registry is required")
	}
	if config.TLS.IssuerEmail == "" {
		return nil, errors.New("existing-cluster config: tls.issuerEmail is required")
	}
	if config.Admin.Email == "" {
		return nil, errors.New("existing-cluster config: admin.email is required")
	}
	if config.Admin.PasswordFile == "" {
		return nil, errors.New("existing-cluster config: admin.passwordFile is required")
	}
	if config.Skalid.Image == "" {
		return nil, errors.New("existing-cluster config: skalid.image is required")
	}
	if config.Ingress.ClassName == "" {
		return nil, errors.New("existing-cluster config: ingress.className is required")
	}

	if config.Database.Tier == "" {
		config.Database.Tier = string(layout.TierSingle)
	}
	switch layout.Tier(config.Database.Tier) {
	case layout.TierSingle, layout.TierAsynchronous, layout.TierSynchronous:
	default:
		return nil, fmt.Errorf("existing-cluster config: database.tier must be single, asynchronous, or synchronous, got %q",
			config.Database.Tier)
	}
	if config.Database.Storage == "" {
		config.Database.Storage = DefaultDatabaseStorage
	}
	if config.Registry.Storage == "" {
		config.Registry.Storage = DefaultRegistryStorage
	}
	for _, field := range []struct{ name, value string }{
		{"database.storage", config.Database.Storage},
		{"registry.storage", config.Registry.Storage},
	} {
		if _, err := resource.ParseQuantity(field.value); err != nil {
			return nil, fmt.Errorf("existing-cluster config: %s %q is not a valid quantity", field.name, field.value)
		}
	}

	config.Operators.CNPG = defaultOperator(config.Operators.CNPG)
	config.Operators.CertManager = defaultOperator(config.Operators.CertManager)
	for _, field := range []struct{ name, value string }{
		{"operators.cnpg", config.Operators.CNPG},
		{"operators.certManager", config.Operators.CertManager},
	} {
		if field.value != "install" && field.value != "use-existing" {
			return nil, fmt.Errorf("existing-cluster config: %s must be install or use-existing, got %q",
				field.name, field.value)
		}
	}

	if len(config.Capabilities) == 0 {
		config.Capabilities = append([]string(nil), layout.RequiredCapabilities...)
	}
	seen := map[string]bool{}
	deduped := config.Capabilities[:0]
	for _, capability := range config.Capabilities {
		if !slices.Contains(layout.Capabilities, capability) {
			return nil, fmt.Errorf("existing-cluster config: unknown capability %q", capability)
		}
		if !seen[capability] {
			seen[capability] = true
			deduped = append(deduped, capability)
		}
	}
	config.Capabilities = deduped
	return &config, nil
}

func defaultOperator(value string) string {
	if value == "" {
		return "install"
	}
	return value
}
