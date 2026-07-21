package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

// JoinTokenTTL is the fixed lifetime of a generated join token.
const JoinTokenTTL = "24h"

// JoinToken is what `skali-installer token` hands to the operator.
type JoinToken struct {
	Cluster   string
	ServerURL string
	Token     string
}

// CreateJoinToken mints a time-limited agent join token on a server node
// via `k3s token create`. Initialization is deliberately not required:
// nodes join before init runs.
func CreateJoinToken(ctx context.Context, runner host.Runner, record *Record) (*JoinToken, error) {
	if record.Node.Role != layout.RoleServer {
		return nil, fmt.Errorf("join tokens are created on a server node")
	}
	result, err := runner.Run(ctx, host.Command{
		Name: "k3s", Args: []string{"token", "create", "--ttl", JoinTokenTTL},
	})
	if err != nil {
		return nil, fmt.Errorf("run k3s token create: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("k3s token create failed with exit code %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	token := lastNonEmptyLine(result.Stdout)
	if token == "" {
		return nil, fmt.Errorf("k3s token create produced no token")
	}
	return &JoinToken{
		Cluster:   record.Cluster,
		ServerURL: "https://" + net.JoinHostPort(serverJoinHost(ctx, runner, record.Node.Name), "6443"),
		Token:     token,
	}, nil
}

// serverJoinHost resolves the address agents should join through: the
// node's InternalIP, falling back to the node name (the hostname) when the
// lookup fails.
func serverJoinHost(ctx context.Context, runner host.Runner, nodeName string) string {
	result, err := runner.Run(ctx, host.Command{
		Name: "k3s", Args: []string{"kubectl", "get", "node", nodeName, "-o", "json"},
	})
	if err != nil || result.ExitCode != 0 {
		return nodeName
	}
	var node struct {
		Status struct {
			Addresses []struct {
				Type    string `json:"type"`
				Address string `json:"address"`
			} `json:"addresses"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &node); err != nil {
		return nodeName
	}
	for _, address := range node.Status.Addresses {
		if address.Type == "InternalIP" && address.Address != "" {
			return address.Address
		}
	}
	return nodeName
}

// lastNonEmptyLine tolerates log preamble ahead of the token itself.
func lastNonEmptyLine(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
