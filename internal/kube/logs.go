package kube

import (
	"bufio"
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TailJobLogs returns the last tail lines of the newest pod of one Job, for
// release-command failure diagnostics in the run journal. A Job without
// pods (deleted, or never scheduled) returns no lines and no error.
func (c *Client) TailJobLogs(ctx context.Context, namespace, jobName string, tail int64) ([]string, error) {
	pods, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "batch.kubernetes.io/job-name=" + jobName,
	})
	if err != nil {
		return nil, fmt.Errorf("kube: list pods of job %s: %w", jobName, err)
	}
	if len(pods.Items) == 0 {
		return nil, nil
	}
	newest := 0
	for index := range pods.Items {
		if pods.Items[index].CreationTimestamp.After(pods.Items[newest].CreationTimestamp.Time) {
			newest = index
		}
	}
	stream, err := c.Clientset.CoreV1().Pods(namespace).
		GetLogs(pods.Items[newest].Name, &corev1.PodLogOptions{TailLines: &tail}).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("kube: read logs of pod %s: %w", pods.Items[newest].Name, err)
	}
	defer stream.Close()
	var lines []string
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}
