package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/Hinkolas/skali/internal/kube"
)

// One Traefik pod per edge node: a stalled metrics endpoint on one node must
// neither hold up the tick past its own scrape bound nor cost the healthy
// node its sample.
func TestSampleEdgeBoundsStalledPod(t *testing.T) {
	previous := edgeScrapeTimeout
	edgeScrapeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { edgeScrapeTimeout = previous })

	running := func(name, uid string) corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system", UID: types.UID(uid)},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/namespaces/kube-system/pods":
			require.Equal(t, "app.kubernetes.io/name=traefik", r.URL.Query().Get("labelSelector"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(corev1.PodList{
				TypeMeta: metav1.TypeMeta{Kind: "PodList", APIVersion: "v1"},
				Items:    []corev1.Pod{running("traefik-healthy", "uid-healthy"), running("traefik-stalled", "uid-stalled")},
			})
		case r.URL.Path == "/api/v1/namespaces/kube-system/pods/traefik-healthy:9100/proxy/metrics":
			_, _ = w.Write([]byte(traefikFixture))
		case r.URL.Path == "/api/v1/namespaces/kube-system/pods/traefik-stalled:9100/proxy/metrics":
			<-r.Context().Done()
		case strings.HasPrefix(r.URL.Path, "/apis/traefik.io/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"apiVersion":"traefik.io/v1alpha1","kind":"IngressRouteList","metadata":{},"items":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := kube.NewFromConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	sampler := &Sampler{Kube: client}

	started := time.Now()
	err = sampler.sampleEdge(context.Background(), time.Now())
	require.Less(t, time.Since(started), 2*time.Second, "the stalled pod is bounded by its own scrape timeout")
	require.Error(t, err, "the stalled scrape is still reported")
	require.Contains(t, sampler.lastEdge, "uid-healthy", "the healthy pod's baseline is recorded despite the stall")
	require.NotContains(t, sampler.lastEdge, "uid-stalled")
}
