package mirror

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/engine/enginetest"
)

// testIssuer self-signs real certificates — writeMaterial parses them, so
// stub PEM won't do. Chain validity doesn't matter here (nothing verifies).
type testIssuer struct {
	t *testing.T
}

func (i testIssuer) ServerPEM(cn string, hosts []string, lifetime time.Duration) ([]byte, []byte, error) {
	i.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(i.t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(lifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(i.t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(i.t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

func (i testIssuer) ClientPEM() ([]byte, []byte, error) {
	return i.ServerPEM("test-client", nil, time.Hour)
}

func (i testIssuer) CAPEM() []byte {
	return []byte("-----BEGIN CERTIFICATE-----\nfake-ca\n-----END CERTIFICATE-----\n")
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Endpoint: "10.0.0.1:5000",
		DataDir:  t.TempDir(),
		Image:    "registry:3",
		Port:     5000,
	}
}

func findByComponent(t *testing.T, fake *enginetest.Fake) engine.Container {
	t.Helper()
	list, err := fake.List(context.Background())
	require.NoError(t, err)
	for _, c := range list {
		if c.Labels[engine.LabelComponent] == Component {
			return c
		}
	}
	t.Fatal("no registry container on the fake engine")
	return engine.Container{}
}

func TestEnsureCreatesRegistry(t *testing.T) {
	ctx := context.Background()
	fake := enginetest.New() // image absent: Deploy must pull it
	cfg := testConfig(t)

	require.NoError(t, ensure(ctx, fake, testIssuer{t}, cfg))

	c := findByComponent(t, fake)
	require.Equal(t, "skali-registry", c.Name)
	require.Equal(t, "running", c.State)
	require.Equal(t, cfg.Image, c.Image)
	require.Equal(t, engine.KindSystem, c.Labels[engine.LabelKind])
	require.Equal(t, "true", c.Labels[engine.LabelManaged])
	require.NotEmpty(t, c.Labels[labelConfigHash])
	require.Equal(t, []string{cfg.Image}, fake.Pulled)

	// Material landed with the right shapes.
	dir := materialDir(cfg)
	for _, f := range []string{"config.yml", "registry.crt", "registry.key", "ca.crt"} {
		_, err := os.Stat(filepath.Join(dir, f))
		require.NoError(t, err, f)
	}
	cfgYML, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	require.NoError(t, err)
	require.Contains(t, string(cfgYML), "clientcas", "mTLS must be required")
	require.Contains(t, string(cfgYML), "enabled: true", "manifest deletes must be enabled")
}

func TestEnsureIsIdempotent(t *testing.T) {
	ctx := context.Background()
	fake := enginetest.New()
	cfg := testConfig(t)

	require.NoError(t, ensure(ctx, fake, testIssuer{t}, cfg))
	first := findByComponent(t, fake)
	cert1, err := os.ReadFile(filepath.Join(materialDir(cfg), "registry.crt"))
	require.NoError(t, err)

	require.NoError(t, ensure(ctx, fake, testIssuer{t}, cfg))
	second := findByComponent(t, fake)
	require.Equal(t, first.ID, second.ID, "matching hash must not recreate")
	cert2, err := os.ReadFile(filepath.Join(materialDir(cfg), "registry.crt"))
	require.NoError(t, err)
	require.Equal(t, cert1, cert2, "valid cert must be reused, not reissued")
}

func TestEnsureRestartsStoppedRegistry(t *testing.T) {
	ctx := context.Background()
	fake := enginetest.New()
	cfg := testConfig(t)

	require.NoError(t, ensure(ctx, fake, testIssuer{t}, cfg))
	c := findByComponent(t, fake)
	require.NoError(t, fake.Stop(ctx, c.ID, 0))

	require.NoError(t, ensure(ctx, fake, testIssuer{t}, cfg))
	again := findByComponent(t, fake)
	require.Equal(t, c.ID, again.ID)
	require.Equal(t, "running", again.State)
}

func TestEnsureRecreatesOnConfigChange(t *testing.T) {
	ctx := context.Background()
	fake := enginetest.New()
	cfg := testConfig(t)

	require.NoError(t, ensure(ctx, fake, testIssuer{t}, cfg))
	old := findByComponent(t, fake)

	cfg.Port = 5001 // config drift: the spec hash changes
	require.NoError(t, ensure(ctx, fake, testIssuer{t}, cfg))
	fresh := findByComponent(t, fake)
	require.NotEqual(t, old.ID, fresh.ID, "hash mismatch must recreate")
	require.Equal(t, "running", fresh.State)
	_, exists := fake.Get(old.ID)
	require.False(t, exists, "the old container must be removed")
}

func TestEnsureErrorClasses(t *testing.T) {
	ctx := context.Background()
	iss := testIssuer{t}

	// Engine down → retryable (not errMaterial).
	fake := enginetest.New()
	fake.ListErr = engine.ErrEngineUnavailable
	err := ensure(ctx, fake, iss, testConfig(t))
	require.Error(t, err)
	require.NotErrorIs(t, err, errMaterial)

	// Unwritable material dir → errMaterial (EnsureLoop gives up).
	cfg := testConfig(t)
	blocker := filepath.Join(cfg.DataDir, "blocked")
	require.NoError(t, os.WriteFile(blocker, []byte("file, not dir"), 0o600))
	cfg.DataDir = blocker
	err = ensure(ctx, enginetest.New(), iss, cfg)
	require.ErrorIs(t, err, errMaterial)
}
