package kube

import (
	"bytes"
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/streaming/pkg/httpstream"
)

// ExecInPod runs one command in the first ready pod matching the selector
// and returns its stdout. The exec subresource rides the API server like
// everything else skalid does. Built for the object store's identity
// administration (`weed shell s3.configure`): SeaweedFS applies identity
// changes through the filer's admin channel, not file writes.
func (c *Client) ExecInPod(ctx context.Context, namespace, selector, container string, command []string) (string, error) {
	pods, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return "", fmt.Errorf("kube: list pods %s [%s]: %w", namespace, selector, err)
	}
	var pod string
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				pod = p.Name
			}
		}
		if pod != "" {
			break
		}
	}
	if pod == "" {
		return "", fmt.Errorf("kube: no ready pod matches %s in %s", selector, namespace)
	}

	req := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	// WebSocket first with SPDY fallback, exactly like kubectl: raw SPDY
	// hangs behind proxies that cannot upgrade it (k3d's nginx-based
	// serverlb is the measured case), while in-cluster both work.
	spdyExec, err := remotecommand.NewSPDYExecutor(c.Config, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("kube: exec executor: %w", err)
	}
	wsExec, err := remotecommand.NewWebSocketExecutor(c.Config, "GET", req.URL().String())
	if err != nil {
		return "", fmt.Errorf("kube: exec websocket executor: %w", err)
	}
	exec, err := remotecommand.NewFallbackExecutor(wsExec, spdyExec, httpstream.IsUpgradeFailure)
	if err != nil {
		return "", fmt.Errorf("kube: exec fallback executor: %w", err)
	}
	var stdout, stderr bytes.Buffer
	if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}); err != nil {
		return "", fmt.Errorf("kube: exec in %s/%s: %w (stderr: %s)", namespace, pod, err, stderr.String())
	}
	return stdout.String(), nil
}
