// Package kubetest gates live cluster tests the way testdb gates database
// tests: TEST_KUBECONFIG points at a disposable cluster (task k3d:up) or
// the tests skip. Nothing here runs against a production cluster.
package kubetest

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const envVar = "TEST_KUBECONFIG"

// Config loads the test cluster's rest.Config or skips the test.
func Config(t *testing.T) *rest.Config {
	t.Helper()
	path := os.Getenv(envVar)
	if path == "" {
		t.Skipf("set %s to run live cluster tests (task k3d:up)", envVar)
	}
	config, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("load %s: %v", envVar, err)
	}
	return config
}

// Clientset builds a typed clientset over Config.
func Clientset(t *testing.T) *kubernetes.Clientset {
	t.Helper()
	clientset, err := kubernetes.NewForConfig(Config(t))
	if err != nil {
		t.Fatalf("build clientset: %v", err)
	}
	return clientset
}

// Namespace creates a uniquely named test namespace and deletes it on
// cleanup, so parallel tests never collide.
func Namespace(t *testing.T, clientset kubernetes.Interface) string {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generate namespace id: %v", err)
	}
	name := fmt.Sprintf("skali-test-%s", id.String()[24:])
	_, err = clientset.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create namespace %s: %v", name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	})
	return name
}

// CountNonWatch wraps the config's transport with a counter of every
// non-watch request, backing the "API reads perform no direct Kubernetes
// request" exit criterion.
func CountNonWatch(config *rest.Config) *atomic.Int64 {
	counter := &atomic.Int64{}
	config.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return countingTransport{base: base, counter: counter}
	})
	return counter
}

type countingTransport struct {
	base    http.RoundTripper
	counter *atomic.Int64
}

func (c countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Query().Get("watch") != "true" {
		c.counter.Add(1)
	}
	return c.base.RoundTrip(request)
}
