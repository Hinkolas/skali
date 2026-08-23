package kube

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// APIServerReady performs one GET of the apiserver's /readyz through the
// same credentials and transport as every other client call. Nil means the
// apiserver answers ready; the error carries the answer (a k3s supervisor
// says 503 "apiserver not ready" while the embedded apiserver is down).
// One request, no retries: callers own any waiting and pass a
// short-deadline context.
func (c *Client) APIServerReady(ctx context.Context) error {
	client, base, err := c.proxyClient()
	if err != nil {
		return err
	}
	u := *base
	u.Path = "/readyz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("kube: readiness request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("kube: apiserver readiness: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("kube: apiserver readiness: %s", readinessAnswer(resp.Status, string(body)))
}

// readinessAnswer compresses a failed readiness body to what a human can
// act on: the k3s supervisor's short answer stays as is, while the
// apiserver's own verbose check list is reduced to its failing entries.
func readinessAnswer(status, body string) string {
	var failing []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[-]") {
			failing = append(failing, line)
		}
	}
	if len(failing) > 0 {
		return strings.Join(failing, " ")
	}
	answer := strings.Join(strings.Fields(body), " ")
	if answer == "" {
		return status
	}
	if len(answer) > 120 {
		answer = answer[:120] + "..."
	}
	return answer
}
