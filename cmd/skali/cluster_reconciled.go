package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/utils"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// hostdSiblingPaths lists a skali-hostd shipped next to the CLI binary for
// this machine's architecture: the bare name a source build produces (bin/)
// or the goreleaser asset name a release directory holds.
func hostdSiblingPaths(executable string) []string {
	dir := filepath.Dir(executable)
	return []string{
		filepath.Join(dir, "skali-hostd"),
		filepath.Join(dir, installer.HostdAsset(runtime.GOARCH)),
	}
}

// loadHostdBinary resolves the Linux skali-hostd a cluster command installs
// on a node: an explicit --hostd-bin, a copy next to this binary, or, for a
// released CLI, this release's asset downloaded from GitHub, verified
// against its checksums and cached for the next node. The daemon never
// lives on a machine that only runs deployments, so installing the CLI does
// not install it. A development build has no release to fetch and must be
// pointed at a build. out receives a note when a download happens; nil is
// silent.
func loadHostdBinary(ctx context.Context, out io.Writer) ([]byte, string, error) {
	if out == nil {
		out = io.Discard
	}
	candidates := []string{hostdBinFlag}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, hostdSiblingPaths(executable)...)
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return data, path, nil
		}
	}
	release := versionpkg.Version
	if !versionpkg.IsRelease(release) {
		return nil, "", fmt.Errorf("skali-hostd was not found next to this development build (%s); "+
			"run task build for a bin/ copy or pass --hostd-bin", release)
	}
	cacheDir := installer.DefaultHostdCacheDir()
	cachePath := filepath.Join(cacheDir, release, installer.HostdAsset(runtime.GOARCH))
	if data, ok := installer.CachedHostd(cacheDir, release, runtime.GOARCH); ok {
		return data, cachePath, nil
	}
	fmt.Fprintf(out, "downloading %s (%s)\n", installer.HostdAsset(runtime.GOARCH), release)
	data, err := installer.FetchHostd(ctx, nil, releaseBase(), release, runtime.GOARCH, cacheDir)
	if err != nil {
		return nil, "", fmt.Errorf("fetch skali-hostd for %s: %w (pass --hostd-bin to use a local copy)", release, err)
	}
	return data, cachePath, nil
}

// releaseBase is the host release assets download from; SKALI_RELEASE_BASE
// points it at a fake release for tests, the same knob the host daemons
// honor.
func releaseBase() string {
	if base := os.Getenv("SKALI_RELEASE_BASE"); base != "" {
		return base
	}
	return installer.ReleaseBase
}

func bootstrapReconciledSeed(ctx context.Context, record *installer.Record,
	hostdBinary []byte) (returnErr error) {
	if record == nil || !record.Reconciled() || record.Node.Role != layout.RoleServer {
		return errors.New("coordinator bootstrap requires a reconciled seed-server record")
	}
	if record.InstallComplete() && record.Coordinator != nil &&
		record.Coordinator.CAPin != "" {
		return nil
	}
	now := time.Now().UTC().Truncate(time.Second)
	record.Lifecycle = &installer.InstallLifecycle{
		Status: installer.InstallStatusInstalling, Phase: installer.InstallPhaseJoined,
		StartAttempted: true, StartedAt: now, UpdatedAt: now,
	}
	if err := installer.SaveRecord(ctx, runner(), record); err != nil {
		return err
	}
	defer func() {
		if returnErr == nil {
			return
		}
		record.Lifecycle.Status = installer.InstallStatusFailed
		record.Lifecycle.LastError = returnErr.Error()
		record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
		_ = installer.SaveRecord(ctx, runner(), record)
	}()
	client, err := installer.KubeClient(ctx, runner())
	if err != nil {
		return err
	}
	host := record.Node.IP
	if host == "" {
		host = installer.ServerJoinHost(ctx, runner(), record.Node.Name)
	}
	endpoint := "https://" + net.JoinHostPort(host, clusterstate.DefaultCoordinatorPort)
	seed := clusterstate.Node{
		ID: record.Node.ID, InstallationID: record.InstallationID,
		Name: record.Node.Name, Role: record.Node.Role,
		Capabilities: append([]string(nil), record.Node.Capabilities...),
		NodeIP:       record.Node.IP, Coordinator: endpoint,
		AgentVersion: versionpkg.Version, K3sVersion: record.Versions.K3s,
	}
	state, err := clusterstate.NewSeedState(record.Cluster, seed, time.Now())
	if err != nil {
		return err
	}
	store := &clusterstate.Store{Client: client.Clientset}
	trust, err := store.Bootstrap(ctx, state)
	if err != nil {
		return err
	}
	pin, err := store.CAPin(ctx)
	if err != nil {
		return err
	}
	privateKey, csr, err := clusterstate.NewAgentKeyAndCSR(record.Node.ID, record.Node.Name)
	if err != nil {
		return err
	}
	certificate, err := store.SignCSR(ctx, csr, record.Node.ID, record.Node.Name, 7*24*time.Hour)
	if err != nil {
		return err
	}
	if err := installer.StageHostd(ctx, runner(), hostdBinary, true); err != nil {
		return err
	}
	if err := installer.SaveAgentIdentity(ctx, runner(), installer.AgentConfig{
		Version: 1, Cluster: record.Cluster, NodeID: record.Node.ID,
		NodeName:       record.Node.Name,
		InstallationID: record.InstallationID, Endpoints: []string{endpoint},
	}, trust.CACert, certificate, privateKey); err != nil {
		return err
	}
	record.Coordinator = &installer.CoordinatorRecord{
		Endpoints: []string{endpoint}, CAPin: pin, AgentVersion: versionpkg.Version,
	}
	if err := installer.SaveRecord(ctx, runner(), record); err != nil {
		return err
	}
	if err := installer.StartHostd(ctx, runner(), true); err != nil {
		return err
	}
	record.Lifecycle.Status = installer.InstallStatusComplete
	record.Lifecycle.Phase = installer.InstallPhaseComplete
	record.Lifecycle.LastError = ""
	record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	return installer.SaveRecord(ctx, runner(), record)
}

type reconciledEnrollmentOptions struct {
	Server           string
	Token            string
	Capabilities     []string
	Network          installer.NodeNetwork
	RequestedRole    string
	RequestedCluster string
	Interactive      bool
	Runner           host.Runner
	HostdBinary      []byte
	// Out receives progress notes such as a hostd download; nil is silent.
	Out io.Writer
}

func runReconciledEnrollment(ctx context.Context, opts reconciledEnrollmentOptions) (*installer.Record, error) {
	hostRunner := opts.Runner
	if hostRunner == nil {
		hostRunner = runner()
	}
	serverOverride := opts.Server != ""
	detected, err := installer.Detect(ctx, hostRunner)
	if err != nil {
		return nil, err
	}
	var saved *installer.Record
	installationID, nodeID, nodeName := uuid.NewString(), uuid.NewString(), detected.Hostname
	switch detected.State {
	case installer.StateFresh:
	case installer.StateEnrolled, installer.StateInterrupted, installer.StateAgent, installer.StateServer:
		saved = detected.Record
		if saved == nil || !saved.Reconciled() || (saved.RegistrationMayHaveStarted() && detected.State != installer.StateAgent && detected.State != installer.StateServer) {
			return nil, fmt.Errorf("enrollment cannot resume from host state %s", detected.State)
		}
		if saved.Coordinator == nil {
			return nil, errors.New("saved enrollment is missing coordinator trust")
		}
		installationID, nodeID, nodeName = saved.InstallationID, saved.Node.ID, saved.Node.Name
		if len(opts.Capabilities) == 0 {
			opts.Capabilities = slices.Clone(saved.Node.Capabilities)
		}
		if opts.Network.ClusterIP == "" {
			opts.Network.ClusterIP = saved.Node.Network().ClusterIP
		}
		if opts.Network.PublicIPs == nil {
			opts.Network.PublicIPs = saved.Node.Network().PublicIPs
		}
		if opts.Network.ExtraSANs == nil {
			opts.Network.ExtraSANs = saved.Node.Network().ExtraSANs
		}
		if opts.Network.CoordinatorBind == nil {
			opts.Network.CoordinatorBind = saved.Node.Network().CoordinatorBind
		}
		if opts.RequestedRole != "" && opts.RequestedRole != saved.Node.Role {
			return nil, errors.New("requested role does not match saved enrollment")
		}
		if opts.RequestedCluster != "" && opts.RequestedCluster != saved.Cluster {
			return nil, errors.New("requested cluster does not match saved enrollment")
		}
		if !utils.SameStrings(opts.Capabilities, saved.Node.Capabilities) || opts.Network.ClusterIP != saved.Node.Network().ClusterIP || !utils.SameStrings(opts.Network.PublicIPs, saved.Node.Network().PublicIPs) || !utils.SameStrings(opts.Network.ExtraSANs, saved.Node.Network().ExtraSANs) || !utils.SameStrings(opts.Network.CoordinatorBind, saved.Node.Network().CoordinatorBind) {
			return nil, errors.New("resume must use the saved node settings; change capabilities through the cluster plan after enrollment")
		}
	default:
		return nil, fmt.Errorf("enrollment requires a fresh or already-enrolled host, found %s", detected.State)
	}
	if opts.Token != "" {
		parsed, err := clusterstate.ParseToken(opts.Token)
		if err != nil {
			return nil, err
		}
		if saved != nil && saved.Coordinator.CAPin != parsed.CAPin {
			return nil, errors.New("this host is already enrolled with different coordinator trust")
		}
	}
	if saved != nil && (detected.State == installer.StateAgent || detected.State == installer.StateServer) {
		return saved, nil
	}
	hostdBinary := opts.HostdBinary
	if len(hostdBinary) == 0 {
		hostdBinary, _, err = loadHostdBinary(ctx, opts.Out)
	}
	if err != nil {
		return nil, fmt.Errorf("%w\nNo enrollment request was sent.", err)
	}
	// Durable mTLS credentials take precedence over invitation expiry/revocation.
	// This resumes local service startup, not a second enrollment.
	if saved != nil {
		resumed, err := installer.ResumeEnrolledHostd(ctx, hostRunner, saved, hostdBinary, opts.Server)
		if err != nil {
			return nil, fmt.Errorf("resume agent startup: %w; retry with sudo skali cluster join", err)
		}
		if resumed {
			if err := hostRunner.Remove(ctx, installer.AgentPendingTokenPath); err != nil {
				return nil, err
			}
			return saved, nil
		}
	}
	if opts.Token == "" && saved != nil {
		data, err := hostRunner.ReadFile(ctx, installer.AgentPendingTokenPath)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		opts.Token = clusterstate.NormalizeToken(string(data))
	}
	if opts.Token == "" && opts.Interactive {
		opts.Token, err = promptJoinToken(ctx, bufio.NewReader(os.Stdin))
		if err != nil {
			return nil, err
		}
	}
	if opts.Token == "" {
		return nil, errors.New("no saved invitation; pass --token, --token-file, or SKALI_JOIN_TOKEN to start or resume enrollment")
	}
	token, err := clusterstate.ParseToken(opts.Token)
	if err != nil {
		return nil, err
	}
	opts.Token = clusterstate.NormalizeToken(opts.Token)
	if saved != nil && saved.Coordinator.CAPin != token.CAPin {
		return nil, errors.New("this host is already enrolled with different coordinator trust")
	}
	if opts.Server == "" && saved != nil && len(saved.Coordinator.Endpoints) > 0 {
		opts.Server = saved.Coordinator.Endpoints[0]
	}
	if opts.Server == "" && len(token.Coordinators) > 0 {
		opts.Server = token.Coordinators[0]
	}
	if opts.Server == "" && opts.Interactive {
		opts.Server, err = promptSession(os.Stdout, bufio.NewReader(os.Stdin)).Text(ctx, cliprompt.TextOptions{Title: "Coordinator", Description: "This older token has no endpoint. Enter the coordinator host or HTTPS URL."})
		if err != nil {
			return nil, err
		}
	}
	if opts.Server == "" {
		return nil, errors.New("this token has no coordinator endpoint; supply the coordinator argument or --server")
	}
	endpoint, err := clusterstate.NormalizeEndpoint(opts.Server)
	if err != nil {
		return nil, err
	}
	if opts.Network.ClusterIP == "" && !(opts.Interactive && saved == nil) {
		opts.Network.ClusterIP = installer.CoordinatorRouteAddress(ctx, hostRunner, endpoint)
	}
	if opts.Interactive && saved == nil && (opts.Network.ClusterIP == "" || opts.Network.PublicIPs == nil) {
		opts.Network, err = promptJoinNodeNetwork(ctx, os.Stdout, bufio.NewReader(os.Stdin), opts.Network, endpoint)
		if err != nil {
			return nil, err
		}
	}
	if opts.Network.ClusterIP == "" {
		addresses, detectErr := installer.DetectHostAddresses(ctx, hostRunner)
		if detectErr == nil {
			var private []installer.HostAddress
			for _, address := range addresses {
				if address.Private {
					private = append(private, address)
				}
			}
			if len(private) == 1 {
				opts.Network.ClusterIP = private[0].IP
			} else if len(addresses) == 1 {
				opts.Network.ClusterIP = addresses[0].IP
			}
		}
		if opts.Network.ClusterIP == "" {
			return nil, errors.New("this node's cluster address is ambiguous; pass --node-ip")
		}
	}
	network, err := installer.ResolveNodeNetwork(ctx, hostRunner, opts.Network)
	if err != nil {
		return nil, fmt.Errorf("%w\nNo enrollment request was sent.", err)
	}
	if network.ClusterIP == "" {
		return nil, errors.New("cannot determine this node's cluster address; pass --node-ip")
	}
	hostFacts := clusterstate.HostFacts{InstallationID: installationID, NodeID: nodeID, Name: nodeName,
		NodeIP: network.ClusterIP, Capabilities: slices.Clone(opts.Capabilities), AgentVersion: versionpkg.Version}
	client := clusterstate.EnrollmentClient{Endpoint: endpoint, Token: opts.Token,
		OnRetry: func(attempt int, delay time.Duration, err error) {
			fmt.Fprintf(os.Stderr, "  enrollment request interrupted; retry %d in %s\n", attempt, delay.Round(time.Millisecond))
		},
	}
	if !serverOverride {
		client.Endpoints = slices.Clone(token.Coordinators)
		if saved != nil {
			client.Endpoints = append(client.Endpoints, saved.Coordinator.Endpoints...)
		}
	}
	fmt.Fprintln(os.Stderr, "  checking invitation and coordinator")
	requestStarted := time.Now()
	preflight, err := client.Preflight(ctx, hostFacts)
	remainingBudget := max(time.Nanosecond, 2*time.Minute-time.Since(requestStarted))
	if err != nil {
		return nil, fmt.Errorf("%w\n%s", err, enrollmentRecoveryHint(err, saved))
	}
	opts.Capabilities, err = joinCapabilities(ctx, opts.Capabilities, preflight.AllowedCapabilities, opts.Interactive)
	if err != nil {
		return nil, err
	}
	hostFacts.Capabilities = slices.Clone(opts.Capabilities)
	if opts.RequestedRole != "" && opts.RequestedRole != preflight.Role {
		return nil, fmt.Errorf("invitation is for role %s, not %s", preflight.Role, opts.RequestedRole)
	}
	if opts.RequestedCluster != "" && opts.RequestedCluster != preflight.Cluster {
		return nil, fmt.Errorf("invitation is for cluster %q, not %q", preflight.Cluster, opts.RequestedCluster)
	}
	if saved != nil && (saved.Node.Role != preflight.Role || saved.Cluster != preflight.Cluster) {
		return nil, errors.New("invitation does not match saved enrollment role or cluster")
	}
	if err := installer.ValidateEnrolledHost(ctx, hostRunner, preflight.Cluster, preflight.Role, nodeName, network, opts.Capabilities); err != nil {
		return nil, err
	}
	record, err := installer.PrepareEnrollmentRecord(ctx, hostRunner, installer.EnrollmentRecordOptions{
		Cluster: preflight.Cluster, NodeID: nodeID, InstallationID: installationID, NodeName: nodeName,
		Role: preflight.Role, Capabilities: opts.Capabilities, Network: network, Endpoint: endpoint, CAPin: token.CAPin, AgentVersion: versionpkg.Version,
	})
	if err != nil {
		return nil, err
	}
	fail := func(cause error) (*installer.Record, error) {
		// Cancellation must not prevent recording a resumable failure.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		record.Lifecycle.Status = installer.InstallStatusFailed
		record.Lifecycle.LastError = strings.ReplaceAll(cause.Error(), opts.Token, "[redacted]")
		record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
		saveErr := installer.SaveRecord(saveCtx, hostRunner, record)
		if saveErr != nil {
			return nil, fmt.Errorf("%s\nCould not update the recovery journal: %w", record.Lifecycle.LastError, saveErr)
		}
		return nil, fmt.Errorf("%s\nSaved enrollment at phase %s. %s", record.Lifecycle.LastError, record.Lifecycle.Phase, enrollmentRecoveryHint(cause, record))
	}
	if err := hostRunner.MkdirAll(ctx, installer.AgentStateDir, 0o700); err != nil {
		return fail(err)
	}
	if err := hostRunner.ReplaceFile(ctx, installer.AgentPendingTokenPath, "", []byte(opts.Token), 0o600); err != nil {
		return fail(err)
	}
	privateKey, csr, err := installer.EnrollmentCSR(ctx, hostRunner, nodeID, nodeName)
	if err != nil {
		return fail(err)
	}
	if err := installer.StageHostd(ctx, hostRunner, hostdBinary, preflight.Role == layout.RoleServer); err != nil {
		return fail(err)
	}
	fmt.Fprintln(os.Stderr, "  enrolling node (safe to retry if interrupted)")
	client.RetryBudget = remainingBudget
	enrollment, err := client.Enroll(ctx, hostFacts, csr)
	if err != nil {
		var problem *clusterstate.EnrollmentError
		if errors.As(err, &problem) {
			return fail(err)
		}
		return fail(fmt.Errorf("enrollment response not confirmed: %w", err))
	}
	endpoints := enrollment.Coordinators
	if len(endpoints) == 0 {
		endpoints = []string{endpoint}
	}
	if err := installer.SaveAgentIdentity(ctx, hostRunner, installer.AgentConfig{
		Version: 1, Cluster: enrollment.Cluster, NodeID: enrollment.NodeID, NodeName: nodeName,
		InstallationID: installationID, Endpoints: endpoints,
	}, []byte(enrollment.CACert), []byte(enrollment.Certificate), privateKey); err != nil {
		return fail(err)
	}
	if err := hostRunner.Remove(ctx, installer.AgentPendingTokenPath); err != nil {
		return fail(err)
	}
	record.Coordinator.Endpoints = endpoints
	record.Lifecycle.Status = installer.InstallStatusEnrolled
	record.Lifecycle.Phase = installer.InstallPhaseAwaitingApply
	record.Lifecycle.LastError = ""
	record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	if err := installer.SaveRecord(ctx, hostRunner, record); err != nil {
		return fail(err)
	}
	fmt.Fprintln(os.Stderr, "  starting node agent; cluster apply will install k3s")
	if err := installer.StartHostd(ctx, hostRunner, false); err != nil {
		return fail(err)
	}
	return record, nil
}

func enrollmentRecoveryHint(err error, saved *installer.Record) string {
	var problem *clusterstate.EnrollmentError
	if errors.As(err, &problem) {
		switch problem.Code {
		case "invitation_expired", "invitation_revoked":
			return "Create a replacement invitation on a controller with the same role and capabilities, then resume with sudo skali cluster join --token '<replacement-token>'. Keep this node's saved enrollment."
		case "enrollment_cancelled":
			return "This identity was cancelled. Run sudo skali cluster uninstall on this host to clear its enrollment before joining with a new invitation."
		}
	}
	if saved == nil {
		return "Retry the same join command with the same token and node settings."
	}
	return "Resume with sudo skali cluster join."
}

func reconciledToken(value string) bool {
	return strings.HasPrefix(clusterstate.NormalizeToken(value), clusterstate.TokenPrefix)
}

func reconciledClusterStore(ctx context.Context) (*clusterstate.Store, *installer.Record, error) {
	detected, err := installer.Detect(ctx, runner())
	if err != nil {
		return nil, nil, err
	}
	if detected.Record == nil || !detected.Record.Reconciled() ||
		detected.Record.Node.Role != layout.RoleServer {
		return nil, nil, errors.New("this command requires a server in a reconciled cluster")
	}
	client, err := installer.KubeClient(ctx, runner())
	if err != nil {
		return nil, nil, err
	}
	return &clusterstate.Store{Client: client.Clientset}, detected.Record, nil
}

type reconciledInitOperation struct {
	Store        *clusterstate.Store
	OperationID  string
	RegistryNode string
}

func stageReconciledLayout(ctx context.Context, record *installer.Record,
	desired *layout.Layout) error {
	if record == nil || !record.Reconciled() || desired == nil {
		return nil
	}
	store, _, err := reconciledClusterStore(ctx)
	if err != nil {
		return err
	}
	_, err = store.Update(ctx, func(state *clusterstate.State) error {
		_, err := state.ReplaceCandidateLayout(*desired, time.Now())
		return err
	})
	return err
}

// prepareReconciledInit turns initialization into the first declarative
// apply. It freezes all currently staged hosts and capability changes
// together with platform enablement. The coordinator joins those hosts
// first, then intentionally waits for the CLI to collect the one-time
// admin credentials.
func prepareReconciledInit(ctx context.Context, record *installer.Record,
	progress *taskProgress) (*reconciledInitOperation, error) {
	if record == nil || !record.Reconciled() {
		return nil, nil
	}
	store, _, err := reconciledClusterStore(ctx)
	if err != nil {
		return nil, err
	}

	for {
		state, err := store.Load(ctx)
		if err != nil {
			return nil, err
		}
		if state.CurrentOperation == "" {
			break
		}
		operation := state.Operations[state.CurrentOperation]
		target := state.Revisions[operation.TargetRevision]
		if target.Platform.Enabled {
			registryNode, err := registryNodeName(target)
			if err != nil {
				return nil, err
			}
			updated, err := store.Update(ctx, func(current *clusterstate.State) error {
				_, _, err := clusterstate.FreezeCandidate(current,
					operation.RebalanceWorkloads, time.Now())
				return err
			})
			if err != nil {
				return nil, err
			}
			operation = updated.Operations[updated.CurrentOperation]
			if err := waitForInitializationGate(ctx, store, operation.ID, progress); err != nil {
				return nil, err
			}
			return &reconciledInitOperation{
				Store: store, OperationID: operation.ID, RegistryNode: registryNode,
			}, nil
		}
		fmt.Fprintf(os.Stdout, "waiting for active topology operation %s before initialization\n",
			operation.ID)
		if err := waitClusterOperation(ctx, store, operation.ID); err != nil {
			return nil, err
		}
	}

	var operation clusterstate.Operation
	var plan clusterstate.Plan
	var registryNode string
	state, err := store.Update(ctx, func(state *clusterstate.State) error {
		candidate, err := state.Candidate()
		if err != nil {
			return err
		}
		// A released CLI records the platform version it initializes, so
		// the coordinator and the console know what runs and console
		// updates have a baseline; a dev build leaves it unset.
		wantVersion := ""
		if versionpkg.IsRelease(versionpkg.Version) && candidate.Platform.Version == "" {
			wantVersion = versionpkg.Version
		}
		if !candidate.Platform.Enabled || candidate.Platform.RegistryNode == "" || wantVersion != "" {
			_, err = state.EditCandidate(time.Now(),
				func(nodes map[string]clusterstate.RevisionNode,
					platform *clusterstate.PlatformState) error {
					platform.Enabled = true
					if platform.RegistryNode == "" {
						node, err := chooseRegistryNode(nodes)
						if err != nil {
							return err
						}
						platform.RegistryNode = node.ID
					}
					if wantVersion != "" {
						platform.Version = wantVersion
					}
					registry, ok := nodes[platform.RegistryNode]
					if !ok {
						return errors.New("the target registry node is missing")
					}
					registryNode = registry.Name
					return nil
				})
			if err != nil {
				return err
			}
		} else {
			registryNode, err = registryNodeName(candidate)
			if err != nil {
				return err
			}
		}
		plan, operation, err = clusterstate.FreezeCandidate(state, false, time.Now())
		return err
	})
	if err != nil {
		return nil, err
	}
	printClusterPlan(plan)
	if operation.ID == "" {
		// A converged initialized platform may be safely converged again.
		return &reconciledInitOperation{Store: store, RegistryNode: registryNode}, nil
	}
	printAcceptedOperation(operation)
	_ = state
	if err := waitForInitializationGate(ctx, store, operation.ID, progress); err != nil {
		return nil, err
	}
	return &reconciledInitOperation{
		Store: store, OperationID: operation.ID, RegistryNode: registryNode,
	}, nil
}

func chooseRegistryNode(nodes map[string]clusterstate.RevisionNode) (clusterstate.RevisionNode, error) {
	for _, node := range clusterstate.SortedRevisionNodes(nodes) {
		if slices.Contains(node.Capabilities, layout.CapabilityRegistry) {
			return node, nil
		}
	}
	return clusterstate.RevisionNode{}, errors.New(
		"the candidate has no registry-capable node; stage the capability before init")
}

func registryNodeName(revision clusterstate.Revision) (string, error) {
	if revision.Platform.RegistryNode == "" {
		node, err := chooseRegistryNode(revision.Nodes)
		if err != nil {
			return "", err
		}
		return node.Name, nil
	}
	node, ok := revision.Nodes[revision.Platform.RegistryNode]
	if !ok {
		return "", errors.New("the target registry node is missing")
	}
	return node.Name, nil
}

func waitForInitializationGate(ctx context.Context, store *clusterstate.Store,
	operationID string, progress *taskProgress) error {
	progress.Start("Wait for target topology")
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	phase := ""
	for {
		state, err := store.Load(ctx)
		if err != nil {
			return err
		}
		operation, ok := state.Operations[operationID]
		if !ok {
			return errors.New("initialization operation disappeared")
		}
		if operation.Phase != phase {
			phase = operation.Phase
			progress.Note(fmt.Sprintf("cluster operation %s: %s",
				shortRevision(operation.ID), phase))
		}
		switch operation.Phase {
		case clusterstate.OperationInitializing:
			progress.Done("topology ready")
			fmt.Fprintln(os.Stdout, "target topology is ready; initializing the Skali platform")
			return nil
		case clusterstate.OperationComplete:
			progress.Done("already converged")
			return nil
		case clusterstate.OperationFailed:
			return fmt.Errorf("cluster operation failed before initialization: %s",
				operation.LastError)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func finishReconciledInit(ctx context.Context, prepared *reconciledInitOperation) error {
	if prepared == nil || prepared.OperationID == "" {
		return nil
	}
	return waitClusterOperation(ctx, prepared.Store, prepared.OperationID)
}
