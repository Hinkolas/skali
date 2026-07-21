package installer

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
)

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
  - name: default
    cluster:
      server: https://127.0.0.1:6443
      certificate-authority-data: Zm9v
contexts:
  - name: default
    context:
      cluster: default
      user: default
current-context: default
users:
  - name: default
    user:
      token: secret
`

func TestRewriteKubeconfigAddress(t *testing.T) {
	t.Parallel()
	rewritten, err := rewriteKubeconfigAddress([]byte(testKubeconfig), "127.0.0.1:16443")
	require.NoError(t, err)
	require.Contains(t, string(rewritten), "https://127.0.0.1:16443")
	require.NotContains(t, string(rewritten), "https://127.0.0.1:6443")
	// Certificate material and credentials survive the rewrite.
	require.Contains(t, string(rewritten), "Zm9v")
	require.Contains(t, string(rewritten), "secret")
}

func TestRewriteKubeconfigAddressInvalid(t *testing.T) {
	t.Parallel()
	_, err := rewriteKubeconfigAddress([]byte(":"), "127.0.0.1:16443")
	require.ErrorContains(t, err, "parse kubeconfig")
}

// The rewrite triggers only for runners that are not this machine: Local
// must never satisfy the interface, Lima always does.
func TestAPIAddresserImplementations(t *testing.T) {
	t.Parallel()
	var runner host.Runner = host.Local{}
	_, ok := runner.(host.APIAddresser)
	require.False(t, ok)
	runner = host.Lima{Instance: "skali"}
	_, ok = runner.(host.APIAddresser)
	require.True(t, ok)
}
