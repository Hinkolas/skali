package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/version"
)

// InstallOptions parameterize one fresh or resumable node install.
type InstallOptions struct {
	Cluster      string
	Role         string
	Capabilities []string
	// Management selects the version-2 coordinator lifecycle for a new
	// seed. Empty preserves the legacy path for existing call sites.
	Management string
	NodeID     string
	// Pending taints a newly applied reconciled node until the coordinator
	// verifies the complete membership batch.
	Pending bool
	Join    *JoinOptions
	// Network declares this host's addresses. An empty declaration is
	// completed from the host itself, so the advertised address is always
	// explicit rather than whatever k3s happened to pick.
	Network   NodeNetwork
	Endpoints *Endpoints
	TLS       *TLSConfig
	Progress  Progress
	// RecoverOrphan confirms that a recordless host with the complete
	// Skali-specific fingerprint may be converted into a managed
	// interrupted transaction.
	RecoverOrphan bool
}

// JoinOptions point a joining host at an existing server. Token wins over
// TokenFile. Server may be empty when a current composite token carries it.
type JoinOptions struct {
	Server    string
	Token     string
	TokenFile string
	// PullSecret is supplied only by the authenticated coordinator agent
	// protocol. Operator-facing join tokens continue carrying it in their
	// legacy composite payload.
	PullSecret string
}

// Install validates every input and remote join credential before durable
// mutation, then drives a phase-journaled, resumable k3s installation.
func Install(ctx context.Context, runner host.Runner, opts InstallOptions) (*Record, error) {
	progress := opts.Progress
	if progress == nil {
		progress = silentProgress{}
	}
	if err := validateInstallMetadata(opts); err != nil {
		return nil, noChanges(err)
	}
	detected, err := Detect(ctx, runner)
	if err != nil {
		return nil, err
	}
	recoveringOrphan := false
	switch detected.State {
	case StateFresh, StateInterrupted, StateEnrolled:
	case StateOrphaned:
		if !opts.RecoverOrphan {
			return nil, errors.New("an interrupted Skali install from an older version was found; " +
				"confirm recovery from the interactive cluster menu or rerun with matching configuration")
		}
		recoveringOrphan = true
	case StateServer, StateAgent:
		matches, matchErr := installOptionsMatchRecord(ctx, runner, opts, detected.Record)
		if matchErr != nil {
			return nil, noChanges(matchErr)
		}
		if matches {
			return detected.Record, nil
		}
		return nil, fmt.Errorf("this host already carries a complete Skali installation for cluster %q; "+
			"the requested inputs do not match", detected.Record.Cluster)
	case StateUnmanaged:
		return nil, fmt.Errorf("k3s is installed but no skali installation record exists at %s; "+
			"this host is not managed by skali and will not be adopted or destroyed", RecordPath)
	case StateUnsupported:
		return nil, fmt.Errorf("this host cannot run a skali installation: %s",
			joinProblems(detected.Problems))
	default:
		return nil, fmt.Errorf("this Skali installation is damaged: %s; run skali cluster repair or uninstall",
			joinProblems(detected.Problems))
	}

	existing := detected.Record
	if existing != nil {
		seedResumeOptions(&opts, existing)
	}
	resolved, err := resolveAndPreflightInstall(ctx, runner, detected, opts)
	if err != nil {
		return nil, noChanges(err)
	}
	if existing != nil {
		if resolved.Cluster != existing.Cluster || resolved.Role != existing.Node.Role {
			return nil, noChanges(fmt.Errorf("an interrupted install cannot change cluster %q/%s to %q/%s",
				existing.Cluster, existing.Node.Role, resolved.Cluster, resolved.Role))
		}
		if existing.Node.Name != "" && resolved.NodeName != existing.Node.Name {
			return nil, noChanges(fmt.Errorf("an interrupted install belongs to node %q, but this host is now %q",
				existing.Node.Name, resolved.NodeName))
		}
		if existing.Lifecycle != nil && existing.Lifecycle.StartAttempted &&
			!sameCapabilities(existing.Node.Capabilities, resolved.Capabilities) {
			return nil, noChanges(fmt.Errorf("an install that may already have registered cannot change capabilities from %s to %s",
				strings.Join(existing.Node.Capabilities, ", "),
				strings.Join(resolved.Capabilities, ", ")))
		}
	}
	// Fingerprint recovery becomes persistent only after the supplied
	// identity, token, endpoint, and credential all match. A typo in a
	// non-interactive recovery config therefore cannot adopt the host.
	if recoveringOrphan {
		if err := PersistOrphanRecord(ctx, runner, existing); err != nil {
			return nil, err
		}
	}

	node := k3sNode{
		Name:         resolved.NodeName,
		Cluster:      resolved.Cluster,
		Role:         resolved.Role,
		Capabilities: resolved.Capabilities,
		Network:      resolved.Network,
		ServerURL:    resolved.Server,
		Token:        resolved.K3sToken,
		PullSecret:   resolved.PullSecret,
		Pending:      opts.Pending,
	}
	if opts.Join == nil {
		if existingSecret := existingPullSecret(ctx, runner); existingSecret != "" {
			node.PullSecret = existingSecret
		} else {
			node.PullSecret, err = newPullSecret()
			if err != nil {
				return nil, err
			}
		}
	} else if node.PullSecret == "" {
		node.PullSecret = existingPullSecret(ctx, runner)
		if node.PullSecret == "" {
			progress.Start("Store registry pull credential")
			progress.Skip("the join token carries none; pulls from the managed registry will not authenticate")
		}
	}

	record := existing
	now := time.Now().UTC().Truncate(time.Second)
	if record == nil {
		record = &Record{
			Version:        RecordVersion,
			InstallationID: uuid.NewString(),
			Provider:       ProviderK3s,
			Cluster:        resolved.Cluster,
			Ownership:      OwnershipManaged,
			Node: NodeRecord{
				Name:         resolved.NodeName,
				Role:         resolved.Role,
				Capabilities: append([]string(nil), resolved.Capabilities...),
			},
			Endpoints: opts.Endpoints,
			TLS:       opts.TLS,
			Versions: Versions{
				Installer: version.Version,
				K3s:       K3sVersion,
			},
			CreatedAt: now,
		}
		if opts.Management == ManagementReconciled {
			record.Version = RecordVersionReconciled
			record.Management = ManagementReconciled
			record.Node.ID = opts.NodeID
			if record.Node.ID == "" {
				record.Node.ID = uuid.NewString()
			}
		}
		record.Node.SetNetwork(resolved.Network)
		if opts.Join != nil {
			record.Join = &JoinRecord{Server: resolved.Server}
		}
	} else {
		record.Node.SetNetwork(resolved.Network)
		if opts.Management != "" {
			record.Management = opts.Management
		}
		if opts.NodeID != "" {
			record.Node.ID = opts.NodeID
		}
		record.Node.Capabilities = append([]string(nil), resolved.Capabilities...)
		if record.Join != nil {
			record.Join.Server = resolved.Server
		}
		if opts.Endpoints != nil {
			record.Endpoints = opts.Endpoints
		}
		if opts.TLS != nil {
			record.TLS = opts.TLS
		}
	}

	attemptID := uuid.NewString()
	startAttempted := record.Lifecycle != nil && record.Lifecycle.StartAttempted
	record.Lifecycle = &InstallLifecycle{
		Status:         InstallStatusInstalling,
		Phase:          InstallPhasePrepared,
		AttemptID:      attemptID,
		StartAttempted: startAttempted,
		StartedAt:      now,
		UpdatedAt:      now,
	}
	if err := SaveRecord(ctx, runner, record); err != nil {
		return nil, fmt.Errorf("create installation transaction: %w; no k3s changes were made", err)
	}

	log, err := newInstallLog(ctx, runner, attemptID)
	if err != nil {
		return nil, failInstall(ctx, runner, record, node, nil, err)
	}
	record.Lifecycle.LastLog = log.path
	if err := SaveRecord(ctx, runner, record); err != nil {
		return nil, failInstall(ctx, runner, record, node, log, err)
	}
	log.line("preflight complete for cluster=%q role=%s server=%s",
		resolved.Cluster, resolved.Role, orLocal(resolved.Server))
	_ = log.flush(ctx)

	// A previous starting attempt may still be auto-restarting against the
	// old endpoint. Stop it only after the corrected input passes preflight.
	if startAttempted {
		_ = stopK3sService(ctx, runner, resolved.Role)
	}

	if err := configureK3s(ctx, runner, node); err != nil {
		return nil, failInstall(ctx, runner, record, node, log, err)
	}
	advanceInstall(record, InstallPhaseConfigured)
	if err := SaveRecord(ctx, runner, record); err != nil {
		return nil, failInstall(ctx, runner, record, node, log, err)
	}

	// Longhorn needs iscsid on every node that may attach a volume; the
	// step is idempotent, so an install resume simply re-runs it.
	if err := EnsureStoragePrerequisites(ctx, runner, progress); err != nil {
		return nil, failInstall(ctx, runner, record, node, log, err)
	}
	if err := installK3sFiles(ctx, runner, node, progress); err != nil {
		return nil, failInstall(ctx, runner, record, node, log, err)
	}
	advanceInstall(record, InstallPhaseInstalled)
	if err := SaveRecord(ctx, runner, record); err != nil {
		return nil, failInstall(ctx, runner, record, node, log, err)
	}

	record.Lifecycle.StartAttempted = true
	advanceInstall(record, InstallPhaseStarting)
	if err := SaveRecord(ctx, runner, record); err != nil {
		return nil, failInstall(ctx, runner, record, node, log, err)
	}
	if err := startK3s(ctx, runner, resolved.Role, progress); err != nil {
		return nil, failInstall(ctx, runner, record, node, log, err)
	}
	switch {
	case resolved.Role == layout.RoleAgent:
		err = waitAgentJoined(ctx, runner, resolved.Cluster, resolved.NodeName, progress)
	case opts.Join != nil:
		err = waitServerJoined(ctx, runner, resolved.Cluster, resolved.NodeName, resolved.Capabilities, progress)
	default:
		err = waitNodeReady(ctx, runner, resolved.NodeName, resolved.Capabilities, progress)
	}
	if err != nil {
		return nil, failInstall(ctx, runner, record, node, log, err)
	}
	advanceInstall(record, InstallPhaseJoined)
	if err := SaveRecord(ctx, runner, record); err != nil {
		return nil, failInstall(ctx, runner, record, node, log, err)
	}

	progress.Start("Finalize " + RecordPath)
	record.Lifecycle.Status = InstallStatusComplete
	record.Lifecycle.Phase = InstallPhaseComplete
	record.Lifecycle.LastError = ""
	record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	if err := SaveRecord(ctx, runner, record); err != nil {
		return nil, failInstall(ctx, runner, record, node, log, err)
	}
	log.line("installation complete")
	_ = log.flush(ctx)
	progress.Done("")
	return record, nil
}

func seedResumeOptions(opts *InstallOptions, record *Record) {
	if opts.Cluster == "" {
		opts.Cluster = record.Cluster
	}
	// The address declaration was made on this host, at enrollment or at
	// the previous attempt. It outranks anything the caller left empty,
	// which is what carries a coordinator-driven install (the action knows
	// only the cluster address) onto the full local declaration.
	recorded := record.Node.Network()
	if opts.Network.ClusterIP == "" {
		opts.Network.ClusterIP = recorded.ClusterIP
	}
	if len(opts.Network.PublicIPs) == 0 {
		opts.Network.PublicIPs = recorded.PublicIPs
	}
	if len(opts.Network.ExtraSANs) == 0 {
		opts.Network.ExtraSANs = recorded.ExtraSANs
	}
	if len(opts.Network.CoordinatorBind) == 0 {
		opts.Network.CoordinatorBind = recorded.CoordinatorBind
	}
	if opts.Role == "" {
		opts.Role = record.Node.Role
	}
	if len(opts.Capabilities) == 0 {
		opts.Capabilities = append([]string(nil), record.Node.Capabilities...)
	}
	if record.Join != nil {
		if opts.Join == nil {
			opts.Join = &JoinOptions{Server: record.Join.Server, TokenFile: K3sTokenPath}
		} else {
			if opts.Join.Server == "" {
				opts.Join.Server = record.Join.Server
			}
			if opts.Join.Token == "" && opts.Join.TokenFile == "" {
				opts.Join.TokenFile = K3sTokenPath
			}
		}
	}
}

func installOptionsMatchRecord(ctx context.Context, runner host.Runner, opts InstallOptions, record *Record) (bool, error) {
	if record == nil {
		return false, nil
	}
	if opts.Cluster != "" && opts.Cluster != record.Cluster {
		return false, nil
	}
	if opts.Role != "" && opts.Role != record.Node.Role {
		return false, nil
	}
	if len(opts.Capabilities) > 0 && !sameCapabilities(opts.Capabilities, record.Node.Capabilities) {
		return false, nil
	}
	if opts.Join != nil && record.Join == nil {
		return false, nil
	}
	if opts.Join != nil && opts.Join.Server != "" && record.Join != nil &&
		opts.Join.Server != record.Join.Server {
		return false, nil
	}
	if opts.Join != nil {
		raw := strings.TrimSpace(opts.Join.Token)
		if raw == "" && opts.Join.TokenFile != "" {
			data, err := runner.ReadFile(ctx, opts.Join.TokenFile)
			switch {
			case err == nil:
				raw = strings.TrimSpace(string(data))
			case errors.Is(err, fs.ErrNotExist):
				// Completed joins deliberately allow operators to delete
				// the token file. Identity can still match the record.
			default:
				return false, fmt.Errorf("read join token file %s: %w", opts.Join.TokenFile, err)
			}
		}
		if raw != "" {
			decoded, err := decodeJoinTokenClaims(raw)
			if err != nil {
				return false, err
			}
			if decoded.Cluster != "" && decoded.Cluster != record.Cluster {
				return false, nil
			}
			if decoded.Role != "" && decoded.Role != record.Node.Role {
				return false, nil
			}
			if opts.Join.Server == "" && decoded.Server != "" && record.Join != nil &&
				decoded.Server != record.Join.Server {
				return false, nil
			}
		}
	}
	return true, nil
}

func sameCapabilities(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func existingPullSecret(ctx context.Context, runner host.Runner) string {
	data, err := runner.ReadFile(ctx, K3sRegistriesPath)
	if err != nil {
		return ""
	}
	return registriesPullSecret(data)
}

func advanceInstall(record *Record, phase string) {
	record.Lifecycle.Phase = phase
	record.Lifecycle.Status = InstallStatusInstalling
	record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
}

func failInstall(ctx context.Context, runner host.Runner, record *Record, node k3sNode,
	log *installLog, cause error) error {
	rawDiagnostics := ""
	if record.Lifecycle != nil && record.Lifecycle.StartAttempted {
		rawDiagnostics = collectK3sDiagnostics(ctx, runner, record.Node.Role)
		if detail := diagnosticCause(rawDiagnostics); detail != "" {
			cause = fmt.Errorf("%w: %s", cause, detail)
		}
	}
	secrets := installSecrets(node)
	safeCause := redactInstallText(cause.Error(), secrets...)
	if log != nil {
		log.line("failed in phase %s: %s", record.Lifecycle.Phase, safeCause)
		if rawDiagnostics != "" {
			log.line("system diagnostics:\n%s", redactInstallText(rawDiagnostics, secrets...))
		}
		_ = log.flush(context.WithoutCancel(ctx))
	}

	record.Lifecycle.Status = InstallStatusFailed
	record.Lifecycle.LastError = safeCause
	record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	_ = SaveRecord(context.WithoutCancel(ctx), runner, record)

	if !record.Lifecycle.StartAttempted {
		if rollbackErr := rollbackPreStart(context.WithoutCancel(ctx), runner, record.Node.Role); rollbackErr == nil {
			return fmt.Errorf("installation failed before k3s was started and was rolled back: %s", safeCause)
		} else {
			safeRollback := redactInstallText(rollbackErr.Error(), secrets...)
			return fmt.Errorf("installation failed before k3s was started: %s; automatic rollback failed: %s; "+
				"the interrupted installation remains managed and can be resumed, repaired, or uninstalled%s",
				safeCause, safeRollback, logSuffix(log))
		}
	}
	return fmt.Errorf("installation failed in phase %s: %s; the interrupted installation remains managed; "+
		"correct the input and rerun the installer, or run skali cluster repair/uninstall%s",
		record.Lifecycle.Phase, safeCause, logSuffix(log))
}

func installSecrets(node k3sNode) []string {
	secrets := []string{node.Token, node.PullSecret}
	if parsed, err := parseSecureK3sToken(node.Token); err == nil {
		secrets = append(secrets, parsed.Bearer, parsed.Password)
		if parsed.Username != "" && parsed.Password != "" {
			secrets = append(secrets, parsed.Username+":"+parsed.Password)
		}
	}
	return secrets
}

func noChanges(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "no changes were made") {
		return err
	}
	return fmt.Errorf("%w; No changes were made.", err)
}

func rollbackPreStart(ctx context.Context, runner host.Runner, role string) error {
	script := k3sUninstallScript
	if role == layout.RoleAgent {
		script = k3sAgentUninstallScript
	}
	info, err := runner.Stat(ctx, script)
	if err != nil {
		return err
	}
	if info.Exists {
		if err := uninstallK3s(ctx, runner, role); err != nil {
			return err
		}
	} else {
		binary, err := runner.Stat(ctx, K3sBinaryPath)
		if err != nil {
			return err
		}
		if binary.Exists {
			return fmt.Errorf("%s exists but the upstream uninstall script %s does not", K3sBinaryPath, script)
		}
	}
	if err := runner.Remove(ctx, K3sConfigDir); err != nil {
		return err
	}
	return runner.Remove(ctx, StateDir)
}

func stopK3sService(ctx context.Context, runner host.Runner, role string) error {
	unit := "k3s.service"
	if role == layout.RoleAgent {
		unit = "k3s-agent.service"
	}
	result, err := runner.Run(ctx, host.Command{Name: "systemctl", Args: []string{"stop", unit}})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("stop %s: exit %d: %s", unit, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func logSuffix(log *installLog) string {
	if log == nil {
		return ""
	}
	return "; log: " + log.path
}

func orLocal(server string) string {
	if server == "" {
		return "new local cluster"
	}
	return server
}

func joinProblems(problems []string) string {
	if len(problems) == 0 {
		return "unknown problem"
	}
	return strings.Join(problems, "; ")
}
