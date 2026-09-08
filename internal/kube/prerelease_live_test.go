package kube_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"io"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestLivePrereleaseOwnershipAndRoutes(t *testing.T) {
	client, err := kube.NewFromConfig(kubetest.Config(t))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	a, b := uuid.NewString(), uuid.NewString()
	for i, id := range []string{a, b} {
		project, environment := "acme-prod", "blue"
		if i == 1 {
			project, environment = "acme", "prod-blue"
		}
		ns := rendering.RenderNamespace(project, environment, id)
		_, err := client.Apply(ctx, ns, false)
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = client.Clientset.CoreV1().Namespaces().Delete(context.Background(), ns.Name, metav1.DeleteOptions{})
		})
		_, err = client.Apply(ctx, ns, false)
		require.NoError(t, err, "owned namespace apply must be idempotent")
	}
	nsA, nsB := rendering.NamespaceName(a), rendering.NamespaceName(b)
	rogue := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: rendering.EnvironmentSecretName, Namespace: nsA}}
	_, err = client.Clientset.CoreV1().Secrets(nsA).Create(ctx, rogue, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = client.Apply(ctx, rendering.RenderEnvironmentSecret("acme-prod", "blue", a, "", map[string][]byte{"private": []byte("secret")}), true)
	require.ErrorContains(t, err, "foreign or unowned")
	_, err = client.Delete(ctx, kube.ObjectRef{GVK: corev1.SchemeGroupVersion.WithKind("Secret"), Namespace: nsA, Name: rogue.Name})
	require.ErrorContains(t, err, "foreign or unowned")

	// Traffic is opt-in because the test uses an explicitly forwarded Traefik
	// listener on the disposable cluster, never an ambient ingress endpoint.
	endpoint := os.Getenv("TEST_TRAEFIK_URL")
	if endpoint != "" {
		for i, id := range []string{a, b} {
			project, domain := "alpha", "a.example.com"
			extra := "\n      injected:\n        domain: a.example.com\n        port: http\n        path: '/`) || Host(`b.example.com`) && PathPrefix(`/auth'"
			if i == 1 {
				project, domain, extra = "beta", "b.example.com", ""
			}
			doc, err := manifest.Parse([]byte(fmt.Sprintf(`version: "1"
name: %s
applications:
  web:
    image: traefik/whoami:v1.10.2
    ports:
      http: {port: 80}
    routes:
      public: {domain: %s, port: http}%s
`, project, domain, extra)), "skali.yml")
			require.NoError(t, err)
			compiled, err := compiler.Compile(doc)
			require.NoError(t, err)
			objects, err := rendering.Render(compiled, rendering.Options{Namespace: rendering.NamespaceName(id), EnvironmentID: id})
			require.NoError(t, err)
			for _, obj := range objects {
				_, err = client.Apply(ctx, obj, false)
				require.NoError(t, err)
			}
		}
		check := func() bool {
			req, _ := http.NewRequestWithContext(ctx, "GET", endpoint+"/auth", nil)
			req.Host = "b.example.com"
			response, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
			if err != nil {
				return false
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			return response.StatusCode == 200 && bytes.Contains(body, []byte(rendering.ApplicationName("beta", "web")))
		}
		require.Eventually(t, check, 100*time.Second, time.Second, "injected path must not intercept the other environment")
	}
	_, err = client.Delete(ctx, kube.ObjectRef{GVK: corev1.SchemeGroupVersion.WithKind("Namespace"), Name: nsA})
	require.NoError(t, err)
	_, err = client.Clientset.CoreV1().Namespaces().Get(ctx, nsB, metav1.GetOptions{})
	require.NoError(t, err, "purging A must retain B")
}
