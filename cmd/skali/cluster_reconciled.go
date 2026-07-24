package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func loadHostdBinary() ([]byte, string, error) {
	candidates := []string{hostdBinFlag}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(executable), "skali-hostd"),
			filepath.Join(filepath.Dir(executable),
				fmt.Sprintf("skali-hostd_linux_%s", runtime.GOARCH)))
	}
	candidates = append(candidates,
		installer.HostdBinaryPath,
		filepath.Join(os.Getenv("HOME"), ".local", "share", "skali",
			fmt.Sprintf("skali-hostd_linux_%s", runtime.GOARCH)))
	for _, path := range candidates {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return data, path, nil
		}
	}
	return nil, "", errors.New("skali-hostd was not found; install this release again or pass --hostd-bin")
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
	NodeIP           string
	RequestedRole    string
	RequestedCluster string
}

func runReconciledEnrollment(ctx context.Context, opts reconciledEnrollmentOptions) (*installer.Record, error) {
	token, err := clusterstate.ParseToken(opts.Token)
	if err != nil {
		return nil, err
	}
	endpoint, err := clusterstate.NormalizeEndpoint(opts.Server)
	if err != nil {
		return nil, err
	}
	detected, err := installer.Detect(ctx, runner())
	if err != nil {
		return nil, err
	}
	var installationID, nodeID, nodeName string
	var resumeRecord *installer.Record
	var completedRecord *installer.Record
	switch detected.State {
	case installer.StateFresh:
		installationID, nodeID, nodeName = uuid.NewString(), uuid.NewString(), detected.Hostname
	case installer.StateEnrolled, installer.StateInterrupted:
		record := detected.Record
		if record == nil || !record.Reconciled() || record.RegistrationMayHaveStarted() {
			return nil, fmt.Errorf("enrollment cannot resume from host state %s", detected.State)
		}
		installationID, nodeID, nodeName = record.InstallationID, record.Node.ID, record.Node.Name
		resumeRecord = record
		if record.Coordinator == nil || record.Coordinator.CAPin != token.CAPin {
			return nil, errors.New("this host is already enrolled with different coordinator trust")
		}
	case installer.StateAgent, installer.StateServer:
		record := detected.Record
		if record == nil || !record.Reconciled() || record.Join == nil {
			return nil, fmt.Errorf("this host already belongs to a cluster as %s",
				detected.State)
		}
		if record.Coordinator == nil || record.Coordinator.CAPin != token.CAPin {
			return nil, errors.New("this host is already enrolled with different coordinator trust")
		}
		installationID, nodeID, nodeName = record.InstallationID, record.Node.ID,
			record.Node.Name
		completedRecord = record
	default:
		return nil, fmt.Errorf("enrollment requires a fresh or already-enrolled host, found %s", detected.State)
	}
	hostFacts := clusterstate.HostFacts{
		InstallationID: installationID, NodeID: nodeID, Name: nodeName,
		NodeIP: opts.NodeIP, Capabilities: append([]string(nil), opts.Capabilities...),
		AgentVersion: versionpkg.Version,
	}
	client := clusterstate.EnrollmentClient{Endpoint: endpoint, Token: opts.Token}
	preflight, err := client.Preflight(ctx, hostFacts)
	if err != nil {
		return nil, fmt.Errorf("%w\nNo changes were made.", err)
	}
	if opts.RequestedRole != "" && opts.RequestedRole != preflight.Role {
		return nil, fmt.Errorf("invitation is for role %s, not requested role %s\nNo changes were made.",
			preflight.Role, opts.RequestedRole)
	}
	if opts.RequestedCluster != "" && opts.RequestedCluster != preflight.Cluster {
		return nil, fmt.Errorf("coordinator manages cluster %q, not requested cluster %q\nNo changes were made.",
			preflight.Cluster, opts.RequestedCluster)
	}
	if err := installer.ValidateEnrolledHost(ctx, runner(), preflight.Cluster,
		preflight.Role, nodeName, opts.NodeIP, opts.Capabilities); err != nil {
		return nil, fmt.Errorf("%w\nNo changes were made.", err)
	}
	if completedRecord != nil {
		if completedRecord.Cluster != preflight.Cluster ||
			completedRecord.Node.Role != preflight.Role ||
			!sameCapabilitySet(completedRecord.Node.Capabilities, opts.Capabilities) {
			return nil, errors.New(
				"this completed enrollment does not match the requested cluster, role, or capabilities")
		}
		return completedRecord, nil
	}
	hostdBinary, _, err := loadHostdBinary()
	if err != nil {
		return nil, fmt.Errorf("%w\nNo changes were made.", err)
	}
	if resumeRecord != nil {
		resumed, resumeErr := installer.ResumeEnrolledHostd(ctx, runner(), resumeRecord,
			hostdBinary, endpoint)
		if resumeErr != nil {
			return nil, resumeErr
		}
		if resumed {
			return resumeRecord, nil
		}
	}
	privateKey, csr, err := installer.EnrollmentCSR(ctx, runner(), nodeID, nodeName)
	if err != nil {
		return nil, err
	}
	record, err := installer.PrepareEnrollmentRecord(ctx, runner(), installer.EnrollmentRecordOptions{
		Cluster: preflight.Cluster, NodeID: nodeID, InstallationID: installationID,
		NodeName: nodeName, Role: preflight.Role,
		Capabilities: opts.Capabilities, NodeIP: opts.NodeIP,
		Endpoint: endpoint, CAPin: token.CAPin, AgentVersion: versionpkg.Version,
	})
	if err != nil {
		return nil, err
	}
	fail := func(cause error) (*installer.Record, error) {
		record.Lifecycle.Status = installer.InstallStatusFailed
		record.Lifecycle.LastError = cause.Error()
		record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
		_ = installer.SaveRecord(ctx, runner(), record)
		return nil, cause
	}
	if err := installer.StageHostd(ctx, runner(), hostdBinary,
		preflight.Role == layout.RoleServer); err != nil {
		return fail(err)
	}
	enrollment, err := client.Enroll(ctx, hostFacts, csr)
	if err != nil {
		return fail(err)
	}
	endpoints := enrollment.Coordinators
	if len(endpoints) == 0 {
		endpoints = []string{endpoint}
	}
	if err := installer.SaveAgentIdentity(ctx, runner(), installer.AgentConfig{
		Version: 1, Cluster: enrollment.Cluster, NodeID: enrollment.NodeID,
		NodeName:       nodeName,
		InstallationID: installationID, Endpoints: endpoints,
	}, []byte(enrollment.CACert), []byte(enrollment.Certificate), privateKey); err != nil {
		return fail(err)
	}
	record.Coordinator.Endpoints = endpoints
	record.Lifecycle.Status = installer.InstallStatusEnrolled
	record.Lifecycle.Phase = installer.InstallPhaseAwaitingApply
	record.Lifecycle.LastError = ""
	record.Lifecycle.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	if err := installer.SaveRecord(ctx, runner(), record); err != nil {
		return fail(err)
	}
	if err := installer.StartHostd(ctx, runner(), false); err != nil {
		return fail(err)
	}
	return record, nil
}

func reconciledToken(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), clusterstate.TokenPrefix)
}

func sameCapabilitySet(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
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
func prepareReconciledInit(ctx context.Context, record *installer.Record) (*reconciledInitOperation, error) {
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
			if err := waitForInitializationGate(ctx, store, operation.ID); err != nil {
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
		if !candidate.Platform.Enabled || candidate.Platform.RegistryNode == "" {
			_, err = state.EditCandidate(time.Now(),
				func(nodes map[string]clusterstate.RevisionNode,
					platform *clusterstate.PlatformState) error {
					node, err := chooseRegistryNode(nodes)
					if err != nil {
						return err
					}
					platform.Enabled = true
					platform.RegistryNode = node.ID
					registryNode = node.Name
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
	fmt.Fprintf(os.Stdout, "initialization operation %s accepted; target revision %s\n",
		operation.ID, operation.TargetRevision)
	_ = state
	if err := waitForInitializationGate(ctx, store, operation.ID); err != nil {
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
	operationID string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		state, err := store.Load(ctx)
		if err != nil {
			return err
		}
		operation, ok := state.Operations[operationID]
		if !ok {
			return errors.New("initialization operation disappeared")
		}
		switch operation.Phase {
		case clusterstate.OperationInitializing:
			fmt.Fprintln(os.Stdout, "target topology is ready; initializing the Skali platform")
			return nil
		case clusterstate.OperationComplete:
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
