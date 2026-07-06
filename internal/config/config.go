// Package config loads strongly-typed, env-driven configuration for every skali
// binary. Values come from the environment (12-factor); a local .env is loaded
// for developer convenience only and never overrides real environment variables.
package config

import (
	"context"
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/sethvargo/go-envconfig"
)

// Base is configuration shared by every binary.
type Base struct {
	// Core infrastructure.
	DatabaseURL string `env:"DATABASE_URL,required"`

	// Logging. Traces/metrics are configured via the standard OTEL_* env vars,
	// which the OpenTelemetry SDK / autoexport read directly — not here.
	LogLevel  string `env:"LOG_LEVEL,default=info"`
	LogFormat string `env:"LOG_FORMAT,default=text"`
	LogOutput string `env:"LOG_OUTPUT,default=stdout"`
	LogFile   string `env:"LOG_FILE,default=skali.log"`
}

// Validate checks enum-like fields so misconfiguration fails fast at startup.
func (b *Base) Validate() error {
	if err := oneOf("LOG_LEVEL", b.LogLevel, "debug", "info", "warn", "error"); err != nil {
		return err
	}
	if err := oneOf("LOG_FORMAT", b.LogFormat, "text", "json"); err != nil {
		return err
	}
	return oneOf("LOG_OUTPUT", b.LogOutput, "stdout", "file", "both")
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
