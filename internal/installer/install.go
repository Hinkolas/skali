package installer

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/version"
)

// InstallOptions parameterize one fresh-node install.
type InstallOptions struct {
	// Cluster names the installation this first server creates.
	Cluster string
	// Capabilities designates what this node runs.
	Capabilities []string
	// Endpoints and TLS are recorded when gathered interactively so a
	// following init can default from the record; nil when unknown.
	Endpoints *Endpoints
	TLS       *TLSConfig
	// Progress narrates the install stages.
	Progress Progress
}

// Install provisions a fresh single-node k3s server and writes the
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

	cluster := opts.Cluster
	if cluster == "" {
		cluster = DefaultCluster
	}
	if len(opts.Capabilities) == 0 {
		return nil, fmt.Errorf("at least one capability is required")
	}

	nodeName := detected.Hostname
	if nodeName == "" {
		return nil, fmt.Errorf("could not determine the hostname for node naming")
	}

	if err := installK3s(ctx, runner, nodeName, cluster, opts.Capabilities, progress); err != nil {
		return nil, err
	}
	if err := waitNodeReady(ctx, runner, opts.Capabilities, progress); err != nil {
		return nil, err
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
			Role:         layout.RoleServer,
			Capabilities: opts.Capabilities,
		},
		Endpoints: opts.Endpoints,
		TLS:       opts.TLS,
		Versions: Versions{
			Installer: version.Version,
			K3s:       K3sVersion,
		},
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
