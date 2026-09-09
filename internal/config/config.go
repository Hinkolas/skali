// Package config loads strongly-typed, env-driven configuration for every skali
// binary. Values come from the environment (12-factor); a local .env is loaded
// for developer convenience only and never overrides real environment variables.
package config

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/joho/godotenv"
	"github.com/sethvargo/go-envconfig"

	"github.com/Hinkolas/skali/internal/proxytrust"
)

// Logging is shared by every binary, including ones with no database access.
// Traces/metrics are configured via the standard OTEL_* env vars, which the
// OpenTelemetry SDK / autoexport read directly — not here.
type Logging struct {
	LogLevel  string `env:"LOG_LEVEL,default=info"`
	LogFormat string `env:"LOG_FORMAT,default=text"`
	LogOutput string `env:"LOG_OUTPUT,default=stdout"`
	LogFile   string `env:"LOG_FILE,default=skali.log"`
}

// Validate checks enum-like fields so misconfiguration fails fast at startup.
func (l *Logging) Validate() error {
	if err := oneOf("LOG_LEVEL", l.LogLevel, "debug", "info", "warn", "error"); err != nil {
		return err
	}
	if err := oneOf("LOG_FORMAT", l.LogFormat, "text", "json"); err != nil {
		return err
	}
	return oneOf("LOG_OUTPUT", l.LogOutput, "stdout", "file", "both")
}

// Base is configuration shared by every command that talks to the control-plane
// database (serve, migrate, user).
type Base struct {
	// Core infrastructure.
	DatabaseURL string `env:"DATABASE_URL,required"`

	Logging
}

// Validate shadows Logging.Validate, so it must chain to it explicitly.
func (b *Base) Validate() error {
	return b.Logging.Validate()
}

// API is the configuration for cmd/skalid. The default port avoids 7000,
// which macOS AirPlay squats on dev machines.
type API struct {
	Base
	HTTPAddr string `env:"HTTP_ADDR,default=:7070"`

	// AuthSecret encrypts TOTP secrets, environment values, and backup-target
	// credentials. Preserve it with system-database backups. Changing it without
	// re-encrypting existing records loses access to those records; no supported
	// key-rotation command exists.
	AuthSecret string `env:"AUTH_SECRET,required"`

	// Additional trusted reverse proxies, as comma-separated CIDRs. Empty trusts
	// only discovered platform proxies when connected to Kubernetes.
	TrustedProxies string `env:"SKALI_TRUSTED_PROXIES"`

	// InstanceName is the operator-chosen display name for this installation,
	// shown by clients (the web shell's org slot); empty leaves naming to the
	// client.
	InstanceName string `env:"SKALI_INSTANCE_NAME,default="`

	// ReauthWindow is how long a session stays "fresh" for sudo-gated
	// endpoints after login or an explicit reauthentication.
	ReauthWindow time.Duration `env:"REAUTH_WINDOW,default=15m"`

	// KubeconfigPath selects the cluster skalid reconciles. Set: the file must
	// load or startup fails. Unset: in-cluster config is attempted; when that
	// also fails skalid runs API-only with observation unknown. The ambient
	// KUBECONFIG variable is deliberately ignored so a server daemon can never
	// silently attach to whatever cluster the developer's shell points at.
	KubeconfigPath string `env:"SKALI_KUBECONFIG,default="`

	// UpdateScan enables the daily release scan behind the console's Updates
	// page; false keeps the daemon free of any outbound request to the
	// release feed (air-gapped installations).
	UpdateScan bool `env:"SKALI_UPDATE_SCAN,default=true"`
	// UpdateFeedURL is the releases listing the scan reads, in the GitHub
	// releases API shape; tests and mirrors point it elsewhere.
	UpdateFeedURL string `env:"SKALI_UPDATE_FEED_URL,default=https://api.github.com/repos/Hinkolas/skali/releases"`

	// ReconcileResync re-fires informer updates for every cached object as the
	// correctness backstop against missed watch edits.
	ReconcileResync time.Duration `env:"RECONCILE_RESYNC_INTERVAL,default=5m"`

	// ReconcileAudit lists all environment targets from the database and
	// enqueues them, catching divergence with no cluster object to fire on.
	ReconcileAudit time.Duration `env:"RECONCILE_AUDIT_INTERVAL,default=30m"`

	// RolloutDeadline bounds how long a promoted revision may stay unhealthy
	// before its run is failed. The target is kept either way; reconciliation
	// stays level-triggered and a late recovery still activates.
	RolloutDeadline time.Duration `env:"RECONCILE_ROLLOUT_DEADLINE,default=10m"`

	// StaleThreshold is how long observation may go without a successful watch
	// re-establishment before sources report stale.
	StaleThreshold time.Duration `env:"OBSERVE_STALE_THRESHOLD,default=30s"`

	// RegistryHost names the managed registry in artifact references (what
	// nodes pull and build clients push, for example localhost:5510). Empty
	// disables the build and import surfaces.
	ReservedHosts []string `env:"SKALI_RESERVED_HOSTS,delimiter=;,default=skali.localhost"`

	RegistryHost string `env:"SKALI_REGISTRY_HOST,default="`

	// RegistryEndpoint is the address skalid itself dials for digest
	// verification (the in-cluster service in bundle installations); empty
	// falls back to RegistryHost.
	RegistryEndpoint string `env:"SKALI_REGISTRY_ENDPOINT,default="`

	// RegistryPushHost names the registry in push references handed to
	// build clients (the public registry domain in production, where
	// RegistryHost is an in-cluster-only name); empty falls back to
	// RegistryHost.
	RegistryPushHost string `env:"SKALI_REGISTRY_PUSH_HOST,default="`

	// RegistryInsecure permits plain HTTP toward the registry; the
	// anonymous loopback-only local registry needs it.
	RegistryInsecure bool `env:"SKALI_REGISTRY_INSECURE,default=false"`

	// RegistryTokenKey is the PEM signing key for registry tokens. Set on
	// production installations, where the registry requires token auth;
	// empty leaves the token endpoint unregistered (the anonymous local
	// registry).
	RegistryTokenKey string `env:"SKALI_REGISTRY_TOKEN_KEY,default="`

	// RegistryNodeSecret is the shared credential containerd presents (as
	// user skali-node) when nodes pull from the managed registry; it earns
	// pull-only tokens. Empty rejects the node user.
	RegistryNodeSecret string `env:"SKALI_REGISTRY_NODE_SECRET,default="`

	// ManagedCluster enables installer-owned capability placement. Local
	// development leaves it false because k3d nodes carry no capability
	// labels.
	ManagedCluster bool `env:"SKALI_MANAGED_CLUSTER,default=false"`

	// StorageClass names the storage class application volume claims
	// request. The bundle sets it to skali-app when the cluster runs the
	// longhorn storage driver; empty keeps claims on the cluster default
	// (local-path), which is both the dev shape and the local driver.
	StorageClass string `env:"SKALI_STORAGE_CLASS,default="`

	// PlatformPreference is the cluster's ordered build platform
	// preference for mixed-architecture clusters, separated by semicolons.
	// The CLI reads it from the environment status and builds each
	// application for the first preferred platform it supports. Order
	// carries meaning; empty keeps multi-arch builds.
	PlatformPreference []string `env:"SKALI_PLATFORM_PREFERENCE,delimiter=;,default="`

	// CertManager reports that the installation runs cert-manager and the
	// managed ClusterIssuer: routes render explicit Certificates, the
	// kernel watches their issuance, and deploys gate on it. Local
	// development leaves it false; the Certificate CRD does not exist
	// there and the edge stays HTTP-only.
	CertManager bool `env:"SKALI_CERT_MANAGER,default=false"`

	// Capabilities lists what this installation can run, separated by
	// semicolons; deployments whose revisions require more are rejected
	// with a clear error instead of stalling. Today installations serve
	// applications and edge routes.
	Capabilities []string `env:"SKALI_CAPABILITIES,delimiter=;,default=application;edge"`

	// S3Domain is the optional public S3 endpoint domain (endpoints.s3 in
	// the installation record): the substrate publishes bucket endpoints on
	// it and serves the S3 API through the edge. Empty keeps bucket access
	// in-cluster.
	S3Domain string `env:"SKALI_S3_DOMAIN,default="`

	// BuildStaleTimeout bounds how long a local build may go without a
	// heartbeat before the sweeper fails it and its deployment.
	BuildStaleTimeout time.Duration `env:"BUILD_STALE_TIMEOUT,default=30m"`

	// BackupWorkerImage overrides the image backup Jobs run the data-mover
	// in; empty resolves the daemon's own Deployment image, which is right
	// everywhere the bundle deployed skalid.
	BackupWorkerImage string `env:"SKALI_BACKUP_WORKER_IMAGE,default="`

	// BackupJobTimeout bounds one backup or restore Job (a database dump,
	// upload, or volume archive) before it fails as stuck.
	BackupJobTimeout time.Duration `env:"SKALI_BACKUP_JOB_TIMEOUT,default=1h"`
}

// Validate shadows Base.Validate, so it must chain to it explicitly.
func (a *API) Validate() error {
	if _, err := proxytrust.Parse(a.TrustedProxies); err != nil {
		return err
	}
	if err := a.Base.Validate(); err != nil {
		return err
	}
	if len(a.AuthSecret) < 32 {
		return fmt.Errorf("AUTH_SECRET: must be at least 32 characters (generate with `openssl rand -base64 32`)")
	}
	if a.ReauthWindow <= 0 {
		return fmt.Errorf("REAUTH_WINDOW: must be positive")
	}
	if a.ReconcileResync <= 0 {
		return fmt.Errorf("RECONCILE_RESYNC_INTERVAL: must be positive")
	}
	if a.ReconcileAudit <= 0 {
		return fmt.Errorf("RECONCILE_AUDIT_INTERVAL: must be positive")
	}
	if a.RolloutDeadline <= 0 {
		return fmt.Errorf("RECONCILE_ROLLOUT_DEADLINE: must be positive")
	}
	if a.StaleThreshold < 5*time.Second {
		return fmt.Errorf("OBSERVE_STALE_THRESHOLD: must be at least 5s")
	}
	if a.BuildStaleTimeout <= 0 {
		return fmt.Errorf("BUILD_STALE_TIMEOUT: must be positive")
	}
	if len(a.Capabilities) == 0 {
		return fmt.Errorf("SKALI_CAPABILITIES: must name at least one capability")
	}
	return nil
}

type validator interface{ Validate() error }

// Load reads the environment (plus a dev .env) into a *T and validates it.
func Load[T any](ctx context.Context) (*T, error) {
	// Dev convenience: load .env if present. Load (not Overload) never clobbers
	// real environment variables, and no-ops when the file is absent.
	_ = godotenv.Load()

	var cfg T
	if err := envconfig.Process(ctx, &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if v, ok := any(&cfg).(validator); ok {
		if err := v.Validate(); err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
	}
	return &cfg, nil
}

func oneOf(name, val string, allowed ...string) error {
	if slices.Contains(allowed, val) {
		return nil
	}
	return fmt.Errorf("%s: %q must be one of %v", name, val, allowed)
}
