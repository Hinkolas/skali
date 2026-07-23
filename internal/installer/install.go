package installer

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/version"
)

// InstallOptions parameterize one fresh-node install.
type InstallOptions struct {
	// Cluster names the installation; the first server creates it, agents
	// record which cluster they enrolled into.
	Cluster string
	// Role is the K3s role: layout.RoleServer (the default when empty) or
	// layout.RoleAgent.
	Role string
	// Capabilities designates what this node runs.
	Capabilities []string
	// Join enrolls this host into an existing cluster; required for
	// agents, optional for servers (absent means the first server, which
	// creates the cluster).
	Join *JoinOptions
	// NodeIP pins the advertised address on multi-homed hosts.
	NodeIP string
	// Endpoints and TLS are recorded when gathered interactively so a
	// following init can default from the record; nil when unknown.
	Endpoints *Endpoints
	TLS       *TLSConfig
	// Progress narrates the install stages.
	Progress Progress
}

// JoinOptions point a joining host at an existing server. Token wins over
// TokenFile; interactive paste supplies Token, config files supply
// TokenFile.
type JoinOptions struct {
	Server    string
	Token     string
	TokenFile string
}

// Install provisions a fresh node, either the single k3s server creating
// the cluster or an agent joining an existing one, and writes the
// root-owned installation record. The caller has already confirmed the
// host state is fresh; Install re-checks the guard rather than trusting
// it.
func Install(ctx context.Context, runner host.Runner, opts InstallOptions) (*Record, error) {
	progress := opts.Progress
	if progress == nil {
		progress = silentProgress{}
	}
	detected, err := Detect(ctx, runner)
	if err != nil {
		return nil, err
	}
	switch detected.State {
	case StateFresh:
	case StateUnmanaged:
		return nil, fmt.Errorf("k3s is installed but no skali installation record exists at %s; "+
			"this host is not managed by skali and will not be adopted or destroyed", RecordPath)
	case StateUnsupported:
		return nil, fmt.Errorf("this host cannot run a skali installation: %s",
			joinProblems(detected.Problems))
	default:
		return nil, fmt.Errorf("this host already carries a skali installation (state %s); "+
			"re-run skali cluster without arguments for maintenance options", detected.State)
	}

	role := opts.Role
	if role == "" {
		role = layout.RoleServer
	}
	if role != layout.RoleServer && role != layout.RoleAgent {
		return nil, fmt.Errorf("role must be server or agent, got %q", role)
	}
	if role == layout.RoleAgent && opts.Join == nil {
		return nil, fmt.Errorf("role agent requires join options pointing at an existing server")
	}
	if opts.Join != nil {
		if !strings.HasPrefix(opts.Join.Server, "https://") {
			return nil, fmt.Errorf("join server must be an https:// URL, got %q", opts.Join.Server)
		}
		if opts.Join.Token == "" && opts.Join.TokenFile == "" {
			return nil, fmt.Errorf("joining requires a token or a token file")
		}
	}

	cluster := opts.Cluster
	if cluster == "" {
		cluster = DefaultCluster
	}
	if len(opts.Capabilities) == 0 {
		return nil, fmt.Errorf("at least one capability is required")
	}
	for _, capability := range opts.Capabilities {
		if !slices.Contains(layout.Capabilities, capability) {
			return nil, fmt.Errorf("unknown capability %q; expected one of %s",
				capability, strings.Join(layout.Capabilities, ", "))
		}
	}

	nodeName := detected.Hostname
	if nodeName == "" {
		return nil, fmt.Errorf("could not determine the hostname for node naming")
	}

	node := k3sNode{
		Name:         nodeName,
		Cluster:      cluster,
		Role:         role,
		Capabilities: opts.Capabilities,
		NodeIP:       opts.NodeIP,
	}
	if opts.Join != nil {
		// Resolve the token before any mutation so a bad path fails with
		// the host untouched.
		node.ServerURL = opts.Join.Server
		token := opts.Join.Token
		if token == "" {
			data, err := runner.ReadFile(ctx, opts.Join.TokenFile)
			if err != nil {
				return nil, fmt.Errorf("read join token file %s: %w", opts.Join.TokenFile, err)
			}
			token = strings.TrimSpace(string(data))
			if token == "" {
				return nil, fmt.Errorf("join token file %s is empty", opts.Join.TokenFile)
			}
		}
		var tokenRole string
		node.Token, node.PullSecret, tokenRole, err = decodeJoinToken(token)
		if err != nil {
			return nil, err
		}
		// A role claim inside the composite token must match the join: a
		// bootstrap (agent) token cannot join a server, and joining an
		// agent with the permanent server token would work but hand the
		// host a far stronger credential than it needs. Raw k3s tokens
		// carry no claim and pass for either role.
		if tokenRole != "" && tokenRole != role {
			return nil, fmt.Errorf("this join token was minted for role %s, not %s; "+
				"mint a matching token with skali cluster token --role %s", tokenRole, role, role)
		}
		if node.PullSecret == "" {
			// Tolerated so a manually minted k3s token still joins, but the
			// gap is put on the record of the run.
			progress.Start("Store registry pull credential")
			progress.Skip("the join token carries none; pulls from the managed registry will not authenticate")
		}
	} else {
		// The first server mints the cluster's shared registry pull
		// credential; `token` hands it to every joining node and init
		// copies it into the cluster for skalid to honor.
		node.PullSecret, err = newPullSecret()
		if err != nil {
			return nil, err
		}
	}

	if err := installK3s(ctx, runner, node, progress); err != nil {
		return nil, err
	}
	switch {
	case role == layout.RoleAgent:
		if err := waitAgentJoined(ctx, runner, cluster, nodeName, progress); err != nil {
			return nil, err
		}
	case opts.Join != nil:
		if err := waitServerJoined(ctx, runner, cluster, nodeName, opts.Capabilities, progress); err != nil {
			return nil, err
		}
	default:
		if err := waitNodeReady(ctx, runner, nodeName, opts.Capabilities, progress); err != nil {
			return nil, err
		}
	}

	progress.Start("Write " + RecordPath)
	record := &Record{
		Version:        RecordVersion,
		InstallationID: uuid.NewString(),
		Provider:       ProviderK3s,
		Cluster:        cluster,
		Ownership:      OwnershipManaged,
		Node: NodeRecord{
			Name:         nodeName,
			Role:         role,
			Capabilities: opts.Capabilities,
		},
		Endpoints: opts.Endpoints,
		TLS:       opts.TLS,
		Versions: Versions{
			Installer: version.Version,
			K3s:       K3sVersion,
		},
	}
	if opts.Join != nil {
		record.Join = &JoinRecord{Server: opts.Join.Server}
	}
	if err := SaveRecord(ctx, runner, record); err != nil {
		return nil, err
	}
	progress.Done("")
	return record, nil
}

func joinProblems(problems []string) string {
	if len(problems) == 0 {
		return "unknown problem"
	}
	return strings.Join(problems, "; ")
}
