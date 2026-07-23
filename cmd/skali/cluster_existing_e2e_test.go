package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClusterExistingEndToEnd installs the Skali bundle into a plain k3d
// cluster through existing-cluster mode: no host lifecycle, only the
// bundle. It proves the converge, the cluster-resident record, repeat
// install idempotence, the mode refusals, and bundle uninstall. It cannot
// prove real ingress TLS, ACME issuance, or a public-domain kubelet pull
// (no public DNS); those are documented gaps. Gated: it creates and
// destroys a k3d cluster and needs docker.
//
//	TEST_SKALI_CLUSTER_EXISTING=1 go test -timeout 20m -count=1 -run TestClusterExisting ./cmd/skali/...
func TestClusterExistingEndToEnd(t *testing.T) {
	if os.Getenv("TEST_SKALI_CLUSTER_EXISTING") == "" {
		t.Skip("set TEST_SKALI_CLUSTER_EXISTING=1 to run the existing-cluster end-to-end suite")
	}
	const cluster = "skali-e2e-existing"
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	binary := filepath.Join(t.TempDir(), "skali")
	build := exec.Command("go", "build", "-o", binary, "./cmd/skali")
	build.Dir = repoRoot
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build skali: %s", out)

	image := exec.Command("docker", "build", "-t", "skalid:dev",
		"-f", filepath.Join(repoRoot, "build", "skalid.Dockerfile"), repoRoot)
	out, err = image.CombinedOutput()
	require.NoError(t, err, "build skalid image: %s", out)

	// A plain k3d cluster: traefik and local-path come by default, exactly
	// the existing cluster this mode targets.
	t.Cleanup(func() { _ = exec.Command("k3d", "cluster", "delete", cluster).Run() })
	out, err = exec.Command("k3d", "cluster", "create", cluster,
		"--kubeconfig-update-default=false", "--kubeconfig-switch-context=false", "--wait").CombinedOutput()
	require.NoError(t, err, "k3d cluster create: %s", out)
	out, err = exec.Command("k3d", "image", "import", "skalid:dev", "-c", cluster).CombinedOutput()
	require.NoError(t, err, "k3d image import: %s", out)

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	kc, err := exec.Command("k3d", "kubeconfig", "get", cluster).Output()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(kubeconfig, kc, 0o600))

	passwordFile := filepath.Join(t.TempDir(), "admin-password")
	require.NoError(t, os.WriteFile(passwordFile, []byte("e2e-admin-password\n"), 0o600))
	configPath := filepath.Join(t.TempDir(), "existing.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(fmt.Sprintf(`endpoints:
  api: skali.existing.test
  registry: registry.existing.test
tls:
  issuerEmail: e2e@existing.test
  acmeServer: https://acme-staging-v02.api.letsencrypt.org/directory
admin:
  email: admin@existing.test
  passwordFile: %s
skalid:
  image: skalid:dev
ingress:
  className: traefik
`, passwordFile)), 0o644))

	run := func(wantErr bool, args ...string) string {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Env = append(os.Environ(), "KUBECONFIG=/dev/null")
		result, err := command.CombinedOutput()
		if wantErr {
			require.Error(t, err, "expected failure; output:\n%s", result)
		} else {
			require.NoError(t, err, "skali %s:\n%s", strings.Join(args, " "), result)
		}
		return string(result)
	}
	base := []string{"cluster", "--mode", "existing-cluster", "--kubeconfig", kubeconfig}
	with := func(args ...string) []string { return append(append([]string(nil), base...), args...) }

	// Install: preflight, converge to a healthy skalid, admin bootstrap.
	installOut := run(false, with("install", "--config", configPath)...)
	require.Contains(t, installOut, "mode: existing cluster")
	require.Contains(t, installOut, "Verify cluster version and storage prerequisites")
	require.Contains(t, installOut, "Skali is ready")

	kubectl := func(args ...string) string {
		t.Helper()
		command := exec.Command("kubectl", args...)
		command.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
		result, err := command.CombinedOutput()
		require.NoError(t, err, "kubectl %s:\n%s", strings.Join(args, " "), result)
		return string(result)
	}

	// The record is cluster-resident, ownership existing-cluster, provider
	// external, and carries no node.
	record := kubectl("get", "configmap", "-n", "skali-system", "skali-installation",
		"-o", "jsonpath={.data.installation\\.yaml}")
	require.Contains(t, record, "ownership: existing-cluster")
	require.Contains(t, record, "provider: external")
	require.Contains(t, record, "ingressClassName: traefik")

	// The bundle hash is stamped and skalid is healthy.
	namespaceJSON := kubectl("get", "namespace", "skali-system", "-o", "json")
	require.Contains(t, namespaceJSON, `"skali.dev/bundle-hash"`)
	require.NotContains(t, namespaceJSON, `"skali.dev/bundle-hash": ""`)

	// Status reads through the kubeconfig alone.
	statusOut := run(false, with("status")...)
	require.Contains(t, statusOut, "existing-cluster Skali installation")
	require.Contains(t, statusOut, "database healthy")
	require.Contains(t, statusOut, "skalid healthy")

	// Repeat install is a no-op converge: the stamped hash is unchanged.
	before := kubectl("get", "namespace", "skali-system", "-o", "jsonpath={.metadata.annotations.skali\\.dev/bundle-hash}")
	run(false, with("install", "--config", configPath)...)
	after := kubectl("get", "namespace", "skali-system", "-o", "jsonpath={.metadata.annotations.skali\\.dev/bundle-hash}")
	require.Equal(t, before, after, "a repeat install must not move the bundle hash")

	// Refusals: node lifecycle never applies in this mode.
	require.Contains(t, run(true, with("init", "--config", configPath)...), "installs and initializes in one step")
	require.Contains(t, run(true, with("join", "--server", "https://cp:6443", "--token-file", "/x",
		"--capabilities", "database")...), "never manages nodes")
	require.Contains(t, run(true, with("token")...), "never manages nodes")
	require.Contains(t, run(true, with("uninstall", "--scope", "node", "--confirm", "production")...),
		"owns only the skali bundle")

	// The kubeconfig contract: no --kubeconfig is a hard error, and it is
	// never taken from the ambient one.
	require.Contains(t, run(true, "cluster", "--mode", "existing-cluster", "status"), "requires --kubeconfig")

	// Bundle uninstall returns the cluster to fresh; k3d survives.
	uninstallOut := run(false, with("uninstall", "--scope", "bundle", "--confirm", "production")...)
	require.Contains(t, uninstallOut, "The Skali bundle is removed")
	_, err = exec.Command("kubectl", "--kubeconfig", kubeconfig, "get", "namespace", "skali-system").CombinedOutput()
	require.Error(t, err, "skali-system must be gone after the bundle uninstall")
	freshOut := run(false, with("status")...)
	require.Contains(t, freshOut, "no Skali bundle is installed")
}
