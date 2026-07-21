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
	// agents, refused for servers in this slice.
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
			"this host is not managed by skali-installer and will not be adopted or destroyed", RecordPath)
	case StateUnsupported:
		return nil, fmt.Errorf("this host cannot run a skali installation: %s",
			joinProblems(detected.Problems))
	default:
		return nil, fmt.Errorf("this host already carries a skali installation (state %s); "+
			"re-run skali-installer without arguments for maintenance options", detected.State)
	}

	role := opts.Role
	if role == "" {
		role = layout.RoleServer
	}
	if role != layout.RoleServer && role != layout.RoleAgent {
		return nil, fmt.Errorf("role must be server or agent, got %q", role)
	}
	if role == layout.RoleServer && opts.Join != nil {
		return nil, fmt.Errorf("joining as an additional server is not implemented in this slice; " +
			"it arrives with a later milestone")
	}
	if role == layout.RoleAgent {
		if opts.Join == nil {
			return nil, fmt.Errorf("role agent requires join options pointing at an existing server")
		}
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
		Capabilities: opts.Capabilities,
		NodeIP:       opts.NodeIP,
	}
	if role == layout.RoleAgent {
		// Resolve the token before any mutation so a bad path fails with
		// the host untouched.
		node.ServerURL = opts.Join.Server
		node.Token = opts.Join.Token
		if node.Token == "" {
			data, err := runner.ReadFile(ctx, opts.Join.TokenFile)
			if err != nil {
				return nil, fmt.Errorf("read join token file %s: %w", opts.Join.TokenFile, err)
			}
			node.Token = strings.TrimSpace(string(data))
			if node.Token == "" {
				return nil, fmt.Errorf("join token file %s is empty", opts.Join.TokenFile)
			}
		}
	}

	if err := installK3s(ctx, runner, node, progress); err != nil {
		return nil, err
	}
	if role == layout.RoleAgent {
		if err := waitAgentJoined(ctx, runner, cluster, nodeName, progress); err != nil {
			return nil, err
		}
	} else {
		if err := waitNodeReady(ctx, runner, opts.Capabilities, progress); err != nil {
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
	if role == layout.RoleAgent {
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
