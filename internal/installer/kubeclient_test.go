package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Hinkolas/skali/internal/kube"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// Older clusters have no policy yet. The joining host still writes a real
// kubeconfig, and its local API explicitly reports the policy absent.
func legacyAdmissionOnStart(t *testing.T, fake *host.Fake) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"NotFound","code":404}`))
	}))
	t.Cleanup(server.Close)
	config := strings.ReplaceAll(testKubeconfig, "https://127.0.0.1:6443", server.URL)
	config = strings.ReplaceAll(config, "      certificate-authority-data: Zm9v\n", "")
	original := fake.Handlers["systemctl"]
	fake.Handlers["systemctl"] = func(cmd host.Command) (host.Result, error) {
		result, err := original(cmd)
		if err == nil && result.ExitCode == 0 && len(cmd.Args) > 0 && cmd.Args[0] == "start" {
			fake.FS[K3sKubeconfigPath] = []byte(config)
		}
		return result, err
	}
}

func TestJoinedServerWaitsForOwnershipEnforcement(t *testing.T) {
	for _, enforced := range []bool{true, false} {
		t.Run(fmt.Sprint(enforced), func(t *testing.T) {
			policy := kube.OwnershipPolicy()
			policy.Generation = 1
			policy.Status = admissionv1.ValidatingAdmissionPolicyStatus{ObservedGeneration: 1, TypeChecking: &admissionv1.TypeChecking{}}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var object any
				switch {
				case strings.Contains(r.URL.Path, "validatingadmissionpolicybindings"):
					object = kube.OwnershipPolicyBinding()
				case strings.Contains(r.URL.Path, "validatingadmissionpolicies"):
					object = policy
				case r.Method == http.MethodGet:
					object = kube.OwnershipProbe()
				default:
					calls++
					if enforced && calls > 1 {
						w.WriteHeader(http.StatusForbidden)
						object = &metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure, Code: 403, Reason: metav1.StatusReasonForbidden, Message: kube.OwnershipPolicyName + ": skali ownership metadata is immutable"}
					} else {
						object = kube.OwnershipProbe()
					}
				}
				_ = json.NewEncoder(w).Encode(object)
			}))
			defer server.Close()
			fake := linuxHost()
			config := strings.ReplaceAll(testKubeconfig, "https://127.0.0.1:6443", server.URL)
			config = strings.ReplaceAll(config, "      certificate-authority-data: Zm9v\n", "")
			fake.FS[K3sKubeconfigPath] = []byte(config)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			err := waitJoinedOwnership(ctx, fake)
			if enforced {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "ownership policy is not enforced")
			}
		})
	}
}
