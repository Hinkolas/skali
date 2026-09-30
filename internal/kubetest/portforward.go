package kubetest

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// PortForward tunnels a local loopback port to one port of the first ready
// pod matching selector and returns the local host:port, closing the
// tunnel on cleanup. Live tests use it where the API server's service
// proxy cannot stand in: a SigV4 signature covers the Host header and the
// path, both of which the proxy rewrites, so signed S3 requests need a
// transport that leaves them alone.
func PortForward(t *testing.T, config *rest.Config, namespace, selector string, port int) string {
	t.Helper()
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("build clientset: %v", err)
	}
	pod := readyPod(t, clientset, namespace, selector)

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		t.Fatalf("port-forward transport: %v", err)
	}
	target := clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, target)

	stop, ready := make(chan struct{}), make(chan struct{})
	forwarder, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", port)}, stop, ready, os.Stderr, os.Stderr)
	if err != nil {
		t.Fatalf("port-forward %s/%s:%d: %v", namespace, pod, port, err)
	}
	errs := make(chan error, 1)
	go func() { errs <- forwarder.ForwardPorts() }()
	t.Cleanup(func() { close(stop) })
	select {
	case <-ready:
	case err := <-errs:
		t.Fatalf("port-forward %s/%s:%d: %v", namespace, pod, port, err)
	case <-time.After(30 * time.Second):
		t.Fatalf("port-forward %s/%s:%d never became ready", namespace, pod, port)
	}
	ports, err := forwarder.GetPorts()
	if err != nil {
		t.Fatalf("port-forward ports: %v", err)
	}
	return fmt.Sprintf("127.0.0.1:%d", ports[0].Local)
}

// readyPod waits for a running, ready pod matching selector: a live test
// reaches for the tunnel right after the workload reports ready, and the
// pod list can lag that by a moment.
func readyPod(t *testing.T, clientset kubernetes.Interface, namespace, selector string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(),
			metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			t.Fatalf("list pods %s [%s]: %v", namespace, selector, err)
		}
		var names []string
		for i := range pods.Items {
			pod := &pods.Items[i]
			names = append(names, pod.Name)
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					return pod.Name
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no ready pod matches %s in %s (saw: %s)", selector, namespace, strings.Join(names, ", "))
		}
		time.Sleep(2 * time.Second)
	}
}
