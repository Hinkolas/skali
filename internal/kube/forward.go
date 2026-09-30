package kube

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// ServiceAddress returns a host:port skalid can dial for one port of an
// in-cluster Service. Inside the cluster that is the Service's DNS name;
// from a laptop (a kubeconfig client) it is a loopback port-forward to a
// ready pod behind the Service, established once and reused. The seam
// exists for the one protocol the API server's service proxy cannot
// carry: SigV4-signed S3 requests, whose signature covers the Host header
// and the path the proxy rewrites. A forward that has died (its pod went
// away) is dropped and re-established on the next call.
func (c *Client) ServiceAddress(ctx context.Context, namespace, service string, port int) (string, error) {
	if c.inCluster {
		return fmt.Sprintf("%s.%s.svc.cluster.local:%d", service, namespace, port), nil
	}
	key := forwardKey(namespace, service, port)
	c.forwardMu.Lock()
	defer c.forwardMu.Unlock()
	if fw := c.forwards[key]; fw != nil {
		select {
		case <-fw.done:
			delete(c.forwards, key)
		default:
			return fw.address, nil
		}
	}
	svc, err := c.Clientset.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("kube: service %s/%s: %w", namespace, service, err)
	}
	selector := labels.SelectorFromSet(svc.Spec.Selector).String()
	fw, err := c.forwardPodPort(ctx, namespace, selector, port)
	if err != nil {
		return "", err
	}
	if c.forwards == nil {
		c.forwards = map[string]*portForward{}
	}
	c.forwards[key] = fw
	return fw.address, nil
}

// ForgetServiceAddress drops a cached forward after a transport failure so
// the next ServiceAddress re-establishes it.
func (c *Client) ForgetServiceAddress(namespace, service string, port int) {
	if c.inCluster {
		return
	}
	c.forwardMu.Lock()
	defer c.forwardMu.Unlock()
	if fw := c.forwards[forwardKey(namespace, service, port)]; fw != nil {
		fw.stop()
		delete(c.forwards, forwardKey(namespace, service, port))
	}
}

// ForwardPodPort tunnels a loopback port to one port of the first ready pod
// matching selector and returns the local host:port with a stop function.
// Live tests use it directly; ServiceAddress uses it behind a Service.
func (c *Client) ForwardPodPort(ctx context.Context, namespace, selector string, port int) (string, func(), error) {
	fw, err := c.forwardPodPort(ctx, namespace, selector, port)
	if err != nil {
		return "", nil, err
	}
	return fw.address, fw.stop, nil
}

type portForward struct {
	address string
	done    chan struct{}
	stop    func()
}

func forwardKey(namespace, service string, port int) string {
	return fmt.Sprintf("%s/%s:%d", namespace, service, port)
}

func (c *Client) forwardPodPort(ctx context.Context, namespace, selector string, port int) (*portForward, error) {
	pod, err := c.readyPod(ctx, namespace, selector)
	if err != nil {
		return nil, err
	}
	transport, upgrader, err := spdy.RoundTripperFor(c.Config)
	if err != nil {
		return nil, fmt.Errorf("kube: port-forward transport: %w", err)
	}
	target := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, target)

	stop, ready := make(chan struct{}), make(chan struct{})
	forwarder, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", port)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("kube: port-forward %s/%s:%d: %w", namespace, pod, port, err)
	}
	done := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		errs <- forwarder.ForwardPorts()
		close(done)
	}()
	var stopOnce sync.Once
	stopFn := func() { stopOnce.Do(func() { close(stop) }) }
	select {
	case <-ready:
	case err := <-errs:
		return nil, fmt.Errorf("kube: port-forward %s/%s:%d: %w", namespace, pod, port, err)
	case <-time.After(30 * time.Second):
		stopFn()
		return nil, fmt.Errorf("kube: port-forward %s/%s:%d never became ready", namespace, pod, port)
	case <-ctx.Done():
		stopFn()
		return nil, ctx.Err()
	}
	ports, err := forwarder.GetPorts()
	if err != nil {
		stopFn()
		return nil, fmt.Errorf("kube: port-forward ports: %w", err)
	}
	return &portForward{
		address: fmt.Sprintf("127.0.0.1:%d", ports[0].Local),
		done:    done,
		stop:    stopFn,
	}, nil
}

// readyPod waits briefly for a running, ready pod matching selector: a
// caller typically reaches for the tunnel right after the workload
// reported ready, and the pod list can lag that by a moment.
func (c *Client) readyPod(ctx context.Context, namespace, selector string) (string, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pods, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return "", fmt.Errorf("kube: list pods %s [%s]: %w", namespace, selector, err)
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
					return pod.Name, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("kube: no ready pod matches %s in %s (saw: %s)", selector, namespace, strings.Join(names, ", "))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
