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

// JoinTokenTTL is the fixed lifetime of a generated agent join token.
// Server tokens are the permanent k3s server credential and never expire.
const JoinTokenTTL = "24h"

// JoinToken is what `skali cluster token` hands to the operator.
type JoinToken struct {
	Cluster   string
	ServerURL string
	Token     string
	// Role is the role the token was minted for: layout.RoleAgent or
	// layout.RoleServer.
	Role string
	// Expires describes the token lifetime for display: JoinTokenTTL for
	// agent tokens, "never" for server tokens.
	Expires string
}

// CreateJoinToken produces a join token on a server node. Agent tokens
// are minted time-limited via `k3s token create`; server tokens read the
// permanent server credential (k3s bootstrap tokens cannot join servers),
// which the caller must surface with explicit warnings. Initialization is
// deliberately not required: nodes join before init runs. The printed
// token is the composite form: the k3s token plus the cluster's registry
// pull credential read back from this server's registries.yaml, so one
// paste enrolls the node for both.
func CreateJoinToken(ctx context.Context, runner host.Runner, record *Record, role string) (*JoinToken, error) {
	return CreateJoinTokenForServer(ctx, runner, record, role, "")
}

// CreateJoinTokenForServer is CreateJoinToken with an optional advertised
// endpoint override for load balancers and alternate routable addresses.
func CreateJoinTokenForServer(ctx context.Context, runner host.Runner, record *Record, role, serverOverride string) (*JoinToken, error) {
	if record.Node.Role != layout.RoleServer {
		return nil, fmt.Errorf("join tokens are created on a server node")
	}
	if role == "" {
		role = layout.RoleAgent
	}
	if role != layout.RoleServer && role != layout.RoleAgent {
		return nil, fmt.Errorf("token role must be server or agent, got %q", role)
	}
	if serverOverride != "" {
		var err error
		serverOverride, err = normalizeJoinServer(serverOverride)
		if err != nil {
			return nil, err
		}
	}
	var token string
	expires := JoinTokenTTL
	if role == layout.RoleServer {
		if !datastoreIsEtcd(ctx, runner) {
			return nil, fmt.Errorf("this server runs on the legacy sqlite datastore, which cannot " +
				"accept additional servers; reinstall the cluster to enable ha")
		}
		data, err := runner.ReadFile(ctx, K3sServerTokenPath)
		if err != nil {
			return nil, fmt.Errorf("read k3s server token: %w", err)
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return nil, fmt.Errorf("k3s server token at %s is empty", K3sServerTokenPath)
		}
		expires = "never"
	} else {
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
		token = lastNonEmptyLine(result.Stdout)
		if token == "" {
			return nil, fmt.Errorf("k3s token create produced no token")
		}
	}
	pullSecret := ""
	if registries, err := runner.ReadFile(ctx, K3sRegistriesPath); err == nil {
		pullSecret = registriesPullSecret(registries)
	}
	serverURL := serverOverride
	if serverURL == "" {
		serverURL = "https://" + net.JoinHostPort(serverJoinHost(ctx, runner, record.Node.Name), "6443")
	}
	return &JoinToken{
		Cluster:   record.Cluster,
		ServerURL: serverURL,
		Token:     encodeJoinTokenWithClaims(token, pullSecret, role, record.Cluster, serverURL),
		Role:      role,
		Expires:   expires,
	}, nil
}

// CountServers counts control-plane members through the server's own
// kubectl, for the even-count quorum warning. Zero with an error means
// the count is unknown.
func CountServers(ctx context.Context, runner host.Runner) (int, error) {
	result, err := runner.Run(ctx, host.Command{
		Name: "k3s", Args: []string{"kubectl", "get", "nodes",
			"-l", "node-role.kubernetes.io/control-plane=true", "-o", "name"},
	})
	if err != nil {
		return 0, fmt.Errorf("run k3s kubectl get nodes: %w", err)
	}
	if result.ExitCode != 0 {
		return 0, fmt.Errorf("k3s kubectl get nodes failed with exit code %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	count := 0
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, nil
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
