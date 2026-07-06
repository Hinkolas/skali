// Package config loads strongly-typed, env-driven configuration for every skali
// binary. Values come from the environment (12-factor); a local .env is loaded
// for developer convenience only and never overrides real environment variables.
package config

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/joho/godotenv"
	"github.com/sethvargo/go-envconfig"
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

// Base is configuration shared by every binary that talks to the control-plane
// database (serve, migrate, user). The worker-side agent/enroll commands use
// Agent instead — they have no database.
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

	// AuthSecret keys everything the auth system encrypts at rest (e.g. TOTP
	// secrets); rotating it forces users to re-enroll 2FA.
	AuthSecret string `env:"AUTH_SECRET,required"`

	// ReauthWindow is how long a session stays "fresh" for sudo-gated
	// endpoints after login or an explicit reauthentication.
	ReauthWindow time.Duration `env:"REAUTH_WINDOW,default=15m"`

	// GRPCAddr is the master's cluster-plane listener (TLS gRPC). It serves
	// node enrollment; workers' NodeService servers use the same default on
	// their side (see Agent).
	GRPCAddr string `env:"GRPC_ADDR,default=:7443"`

	// ClusterAddr is the externally reachable host:port of GRPCAddr — the
	// address baked into rendered `skalid enroll` commands. Optional at boot
	// so existing single-node deploys keep working; minting a join token
	// fails while it is unset.
	ClusterAddr string `env:"CLUSTER_ADDR"`
}

// Validate shadows Base.Validate, so it must chain to it explicitly.
func (a *API) Validate() error {
	if err := a.Base.Validate(); err != nil {
		return err
	}
	if len(a.AuthSecret) < 32 {
		return fmt.Errorf("AUTH_SECRET: must be at least 32 characters (generate with `openssl rand -base64 32`)")
	}
	if a.ReauthWindow <= 0 {
		return fmt.Errorf("REAUTH_WINDOW: must be positive")
	}
	if a.ClusterAddr != "" {
		if _, _, err := net.SplitHostPort(a.ClusterAddr); err != nil {
			return fmt.Errorf("CLUSTER_ADDR: must be host:port (e.g. 10.0.0.1:7443): %w", err)
		}
	}
	return nil
}

// Agent is the configuration for worker-side commands (`skalid enroll`,
// `skalid agent`). Workers are stateless: no database, no auth secret — their
// only persistent state is the node identity under DataDir.
type Agent struct {
	Logging

	// DataDir holds the node identity written by `skalid enroll`
	// (CA cert, node cert+key, node metadata).
	DataDir string `env:"DATA_DIR,default=/var/lib/skalid"`

	// GRPCAddr is the worker's NodeService listener the master dials.
	GRPCAddr string `env:"GRPC_ADDR,default=:7443"`
}

// Validate shadows Logging.Validate, so it must chain to it explicitly.
func (a *Agent) Validate() error {
	if err := a.Logging.Validate(); err != nil {
		return err
	}
	if a.DataDir == "" {
		return fmt.Errorf("DATA_DIR: must not be empty")
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
	for _, a := range allowed {
		if val == a {
			return nil
		}
	}
	return fmt.Errorf("%s: %q must be one of %v", name, val, allowed)
}
