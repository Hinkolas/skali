package mirror

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/Hinkolas/skali/internal/engine"
)

// Component is the registry's skali.component label value.
const Component = "registry"

const (
	containerName = "skali-registry"
	dataVolume    = "skali-registry-data"
	serverCN      = "skali-registry"

	certLifetime = 365 * 24 * time.Hour
	// reissueWindow: a cert this close to expiry is reissued at ensure time.
	// Boot-time reissue IS the rotation story for now — a master that never
	// restarts within the last month of validity will eventually serve an
	// expired cert, which warnWindow makes visible well in advance.
	reissueWindow = 30 * 24 * time.Hour
	warnWindow    = 60 * 24 * time.Hour

	ensureRetry = 30 * time.Second
)

// errMaterial marks failures writing the registry's on-disk material —
// non-retryable (a mac dev box with an unwritable /var/lib/skalid stays
// broken no matter how often we retry).
var errMaterial = errors.New("mirror: write registry material")

// configYML is the registry's full configuration, bind-mounted rather than
// env-injected: clientcas is a list, and lists don't survive env encoding.
// clientcas present ⇒ distribution requires and verifies client certs.
// delete.enabled backs the catalog's manifest deletes; blob GC (the offline
// `registry garbage-collect`) is a later milestone.
const configYML = `version: 0.1
log:
  level: warn
storage:
  filesystem:
    rootdirectory: /var/lib/registry
  delete:
    enabled: true
http:
  addr: :5000
  tls:
    certificate: /etc/skali/registry.crt
    key: /etc/skali/registry.key
    clientcas:
      - /etc/skali/ca.crt
`

// EnsureLoop drives ensure until it succeeds (the engine may still be
// booting when the master starts), then returns — the container's restart
// policy owns keeping it up from there. Material failures end the loop
// immediately: retrying an unwritable data dir helps nobody.
func EnsureLoop(ctx context.Context, eng engine.Engine, iss Issuer, cfg Config) {
	failing := false
	for ctx.Err() == nil {
		err := ensure(ctx, eng, iss, cfg)
		switch {
		case err == nil:
			slog.InfoContext(ctx, "registry container ensured",
				"endpoint", cfg.Endpoint, "image", cfg.Image, "recovered", failing)
			return
		case errors.Is(err, errMaterial):
			slog.WarnContext(ctx, "registry disabled", "err", err)
			return
		default:
			if !failing {
				slog.WarnContext(ctx, "registry ensure failed, retrying", "err", err)
				failing = true
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(ensureRetry):
		}
	}
}

// ensure converges the master's engine on a running registry container built
// from the current material. Idempotent: a running container whose config
// hash matches is left alone.
func ensure(ctx context.Context, eng engine.Engine, iss Issuer, cfg Config) error {
	hash, err := writeMaterial(iss, cfg)
	if err != nil {
		return err
	}

	existing, err := findRegistry(ctx, eng)
	if err != nil {
		return err
	}
	switch {
	case existing != nil && existing.Labels[engine.LabelConfigHash] == hash:
		if existing.State == "running" {
			return nil
		}
		return eng.Start(ctx, existing.ID)
	case existing != nil:
		// Config/cert/image changed: replace. The data volume survives.
		if err := eng.Remove(ctx, existing.ID, true); err != nil {
			return err
		}
	}
	_, err = engine.Deploy(ctx, eng, containerSpec(cfg, hash), engine.PullIfMissing, true)
	return err
}

func findRegistry(ctx context.Context, eng engine.Engine) (*engine.Container, error) {
	list, err := eng.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range list {
		if c.Labels[engine.LabelComponent] == Component {
			return &c, nil
		}
	}
	return nil, nil
}

func containerSpec(cfg Config, hash string) engine.ContainerSpec {
	return engine.ContainerSpec{
		Name:  containerName,
		Image: cfg.Image,
		// Explicit config path: registry:2 and registry:3 default to
		// different locations, ours is neither.
		Command: []string{"serve", "/etc/skali/config.yml"},
		Mounts: []engine.Mount{
			{Type: "bind", Source: materialDir(cfg), Target: "/etc/skali", ReadOnly: true},
			{Type: "volume", Source: dataVolume, Target: "/var/lib/registry"},
		},
		Ports:   []engine.PortBinding{{HostPort: uint16(cfg.Port), ContainerPort: 5000}},
		Restart: engine.RestartAlways,
		Labels: map[string]string{
			engine.LabelKind:       engine.KindSystem,
			engine.LabelComponent:  Component,
			engine.LabelConfigHash: hash,
		},
	}
}

func materialDir(cfg Config) string { return filepath.Join(cfg.DataDir, "registry") }

// writeMaterial lays down <DataDir>/registry/{config.yml,registry.crt,
// registry.key,ca.crt} and returns the config hash the container spec is
// stamped with. The server cert is reused across boots while it matches the
// wanted hosts and stays comfortably clear of expiry.
func writeMaterial(iss Issuer, cfg Config) (hash string, err error) {
	dir := materialDir(cfg)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("%w: %w", errMaterial, err)
	}

	host, _, err := net.SplitHostPort(cfg.Endpoint)
	if err != nil {
		return "", fmt.Errorf("%w: endpoint %q: %w", errMaterial, cfg.Endpoint, err)
	}
	hosts := []string{host, "localhost", "127.0.0.1"}

	certPath, keyPath := filepath.Join(dir, "registry.crt"), filepath.Join(dir, "registry.key")
	leaf := reusableCert(certPath, keyPath, host)
	if leaf == nil {
		certPEM, keyPEM, err := iss.ServerPEM(serverCN, hosts, certLifetime)
		if err != nil {
			return "", fmt.Errorf("%w: %w", errMaterial, err)
		}
		if err := writeFile(certPath, certPEM, 0o644); err != nil {
			return "", err
		}
		if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
			return "", err
		}
		if leaf, err = parseCertPEM(certPEM); err != nil {
			return "", fmt.Errorf("%w: %w", errMaterial, err)
		}
	}
	if until := time.Until(leaf.NotAfter); until < warnWindow {
		slog.Warn("registry certificate nearing expiry; restart skalid to rotate",
			"not_after", leaf.NotAfter, "remaining", until.Round(time.Hour))
	}

	if err := writeFile(filepath.Join(dir, "ca.crt"), iss.CAPEM(), 0o644); err != nil {
		return "", err
	}
	if err := writeFile(filepath.Join(dir, "config.yml"), []byte(configYML), 0o644); err != nil {
		return "", err
	}

	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s|%d", configYML, leaf.SerialNumber.Text(16), cfg.Image, cfg.Port))
	return hex.EncodeToString(sum[:6]), nil
}

// reusableCert loads the previously issued server cert if it still fits:
// parseable, the wanted host among its SANs, and not within the reissue
// window. Anything else returns nil and the caller reissues.
func reusableCert(certPath, keyPath, host string) *x509.Certificate {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil
	}
	if _, err := os.Stat(keyPath); err != nil {
		return nil
	}
	leaf, err := parseCertPEM(certPEM)
	if err != nil {
		return nil
	}
	if time.Now().Add(reissueWindow).After(leaf.NotAfter) {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if !slices.ContainsFunc(leaf.IPAddresses, ip.Equal) {
			return nil
		}
	} else if !slices.Contains(leaf.DNSNames, host) {
		return nil
	}
	return leaf
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("%w: %w", errMaterial, err)
	}
	return nil
}

func parseCertPEM(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}
