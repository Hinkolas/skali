package localdev

// The node container's address on the cluster network is load-bearing:
// k3s registers it as the node IP at creation and fatals on every later
// boot that cannot find an interface with it ("failed to find interface
// with specified node ip"). k3d leaves the address dynamic, and the
// creation-time allocation is an accident of container order (the k3d
// tools helper usually holds the first free one), so a docker daemon
// restart can hand the node a different address and crash-loop k3s while
// docker keeps reporting the container as running. Create pins the
// allocated address as a static IPAM entry; clusters from before the pin
// are repaired in place by ensureNodeReady the day they break.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/kube"
)

// networkName is the docker network k3d creates for the cluster.
func networkName() string { return "k3d-" + ClusterName() }

// serverAlias is k3d's generated node name, kept as a docker DNS alias on
// the renamed node container; a manual network reconnect must restore it.
func serverAlias() string { return "k3d-" + ClusterName() + "-server-0" }

// nodeIPFatal is the k3s networking fatal that follows a node IP moving
// away from its registered address.
const nodeIPFatal = "failed to find interface with specified node ip"

// fatalScanLines is the log-tail depth for spotting the fatal: it fires
// once per boot and a boot logs a few hundred lines, so a probe landing
// mid-boot must look a full cycle back.
const fatalScanLines = 400

// inspectNodeRaw returns the docker-inspect document of the node
// container. Works on stopped and restarting containers too.
func inspectNodeRaw(ctx context.Context) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "docker", "container", "inspect", nodeContainer()).Output()
	if err != nil {
		return nil, fmt.Errorf("localdev: inspect node container: %w", err)
	}
	return out, nil
}

// nodeState is the docker-side lifecycle of the node container; Restarting
// and RestartCount say what Running cannot: whether the restart policy is
// cycling a crash loop.
type nodeState struct {
	Running      bool
	Restarting   bool
	RestartCount int
	ExitCode     int
}

func parseNodeState(raw []byte) (nodeState, error) {
	var doc []struct {
		State struct {
			Running    bool `json:"Running"`
			Restarting bool `json:"Restarting"`
			ExitCode   int  `json:"ExitCode"`
		} `json:"State"`
		RestartCount int `json:"RestartCount"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nodeState{}, fmt.Errorf("localdev: decode container inspect: %w", err)
	}
	if len(doc) == 0 {
		return nodeState{}, fmt.Errorf("localdev: container inspect returned no entries")
	}
	return nodeState{
		Running:      doc[0].State.Running,
		Restarting:   doc[0].State.Restarting,
		RestartCount: doc[0].RestartCount,
		ExitCode:     doc[0].State.ExitCode,
	}, nil
}

// nodeEndpoint is the node container's attachment to the cluster network.
type nodeEndpoint struct {
	IP       string // current address; empty while the container is down
	PinnedIP string // static IPAM pin; empty on an unpinned cluster
}

func parseNodeEndpoint(raw []byte, network string) (nodeEndpoint, error) {
	var doc []struct {
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress  string `json:"IPAddress"`
				IPAMConfig *struct {
					IPv4Address string `json:"IPv4Address"`
				} `json:"IPAMConfig"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nodeEndpoint{}, fmt.Errorf("localdev: decode container inspect: %w", err)
	}
	if len(doc) == 0 {
		return nodeEndpoint{}, fmt.Errorf("localdev: container inspect returned no entries")
	}
	attachment, ok := doc[0].NetworkSettings.Networks[network]
	if !ok {
		return nodeEndpoint{}, fmt.Errorf("localdev: node container is not attached to network %s", network)
	}
	endpoint := nodeEndpoint{IP: attachment.IPAddress}
	if attachment.IPAMConfig != nil {
		endpoint.PinnedIP = attachment.IPAMConfig.IPv4Address
	}
	return endpoint, nil
}

// inspectNetworkRaw returns the docker-inspect document of the cluster
// network.
func inspectNetworkRaw(ctx context.Context) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "docker", "network", "inspect", networkName()).Output()
	if err != nil {
		return nil, fmt.Errorf("localdev: inspect network %s: %w", networkName(), err)
	}
	return out, nil
}

// networkSpec is the cluster network's configuration plus its members
// besides the node itself, keyed by container name with their bare
// addresses.
type networkSpec struct {
	Subnet  string
	Gateway string
	Labels  map[string]string
	Options map[string]string
	Members map[string]string
}

func parseNetworkSpec(raw []byte, node string) (networkSpec, error) {
	var doc []struct {
		IPAM struct {
			Config []struct {
				Subnet  string `json:"Subnet"`
				Gateway string `json:"Gateway"`
			} `json:"Config"`
		} `json:"IPAM"`
		Labels     map[string]string `json:"Labels"`
		Options    map[string]string `json:"Options"`
		Containers map[string]struct {
			Name        string `json:"Name"`
			IPv4Address string `json:"IPv4Address"`
		} `json:"Containers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return networkSpec{}, fmt.Errorf("localdev: decode network inspect: %w", err)
	}
	if len(doc) == 0 {
		return networkSpec{}, fmt.Errorf("localdev: network inspect returned no entries")
	}
	spec := networkSpec{
		Labels:  doc[0].Labels,
		Options: doc[0].Options,
		Members: map[string]string{},
	}
	if len(doc[0].IPAM.Config) > 0 {
		spec.Subnet = doc[0].IPAM.Config[0].Subnet
		spec.Gateway = doc[0].IPAM.Config[0].Gateway
	}
	for _, member := range doc[0].Containers {
		if member.Name == node {
			continue
		}
		address, _, _ := strings.Cut(member.IPv4Address, "/")
		spec.Members[member.Name] = address
	}
	return spec, nil
}

// nodeLogsTail returns the last lines of the node container's log, both
// streams combined (k3s writes to stderr).
func nodeLogsTail(ctx context.Context, lines int) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "logs", "--tail", strconv.Itoa(lines),
		nodeContainer()).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("localdev: node container logs: %w", err)
	}
	return string(out), nil
}

var nodeIPPattern = regexp.MustCompile(`--node-ip=(\d+\.\d+\.\d+\.\d+)`)

// firstNodeIP extracts the address of a "--node-ip=" occurrence in one log
// line, or "" when the line carries none.
func firstNodeIP(line string) string {
	match := nodeIPPattern.FindStringSubmatch(line)
	if match == nil {
		return ""
	}
	return match[1]
}

// hasNodeIPFatal reports whether the log excerpt carries the node-IP
// networking fatal.
func hasNodeIPFatal(logs string) bool {
	return strings.Contains(logs, nodeIPFatal)
}

// registeredNodeIP recovers the node IP k3s registered at creation from
// the docker log history: the FIRST kubelet "--node-ip=" line. Crash-loop
// boots fatal before kubelet starts, so a later occurrence never reflects
// the registration. Returns "" when log rotation ate the history. The
// history can be many megabytes, so the scan streams and stops the child
// on the first match.
func registeredNodeIP(ctx context.Context) (string, error) {
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	cmd := exec.CommandContext(scanCtx, "docker", "logs", nodeContainer())
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("localdev: node container log history: %w", err)
	}
	go func() {
		_ = cmd.Wait()
		_ = pw.Close()
	}()
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		if ip := firstNodeIP(scanner.Text()); ip != "" {
			cancel()
			_ = pr.Close()
			return ip, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("localdev: scan node container log history: %w", err)
	}
	return "", nil
}

func stopNode(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "docker", "stop", nodeContainer()).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: stop node container: %w\n%s", err, out)
	}
	return nil
}

func startNode(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "docker", "start", nodeContainer()).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: start node container: %w\n%s", err, out)
	}
	return nil
}

// connectNodeStatic reattaches the stopped node to the cluster network at
// a fixed address, writing a persistent IPAM pin so the docker daemon
// reassigns the same address on every restart. Older engines refuse
// static addresses on auto-allocated subnets; for those the network is
// recreated with its own settings made explicit, which is safe exactly
// when the node is its only member.
func connectNodeStatic(ctx context.Context, ip string) error {
	// The disconnect only clears an existing attachment; a node already
	// detached (or mid-flap) makes it fail, which is fine.
	_ = exec.CommandContext(ctx, "docker", "network", "disconnect", "-f",
		networkName(), nodeContainer()).Run()
	connect := func() ([]byte, error) {
		return exec.CommandContext(ctx, "docker", "network", "connect",
			"--ip", ip, "--alias", serverAlias(), networkName(), nodeContainer()).CombinedOutput()
	}
	out, err := connect()
	if err == nil {
		return nil
	}
	if !strings.Contains(string(out), "user configured subnets") {
		return fmt.Errorf("localdev: connect node at %s: %w\n%s", ip, err, out)
	}
	if err := recreateNetworkExplicit(ctx); err != nil {
		return err
	}
	if out, err := connect(); err != nil {
		return fmt.Errorf("localdev: connect node at %s: %w\n%s", ip, err, out)
	}
	return nil
}

// recreateNetworkExplicit rebuilds the cluster network with its inspected
// subnet, gateway, labels, and options declared explicitly, turning the
// auto-allocated subnet into a user-configured one that accepts static
// addresses on every engine. Refused while any container is attached.
func recreateNetworkExplicit(ctx context.Context) error {
	raw, err := inspectNetworkRaw(ctx)
	if err != nil {
		return err
	}
	spec, err := parseNetworkSpec(raw, nodeContainer())
	if err != nil {
		return err
	}
	if len(spec.Members) > 0 {
		names := make([]string, 0, len(spec.Members))
		for name := range spec.Members {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf("localdev: cannot rebuild network %s while containers are attached: %s",
			networkName(), strings.Join(names, ", "))
	}
	if spec.Subnet == "" {
		return fmt.Errorf("localdev: network %s reports no subnet", networkName())
	}
	if out, err := exec.CommandContext(ctx, "docker", "network", "rm",
		networkName()).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: remove network %s: %w\n%s", networkName(), err, out)
	}
	args := []string{"network", "create", "--subnet", spec.Subnet}
	if spec.Gateway != "" {
		args = append(args, "--gateway", spec.Gateway)
	}
	for _, key := range sortedKeys(spec.Labels) {
		args = append(args, "--label", key+"="+spec.Labels[key])
	}
	for _, key := range sortedKeys(spec.Options) {
		args = append(args, "--opt", key+"="+spec.Options[key])
	}
	args = append(args, networkName())
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: recreate network %s: %w\n%s", networkName(), err, out)
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// pinNodeIP pins the running node to its current address by a clean stop,
// reattach with the static pin, start cycle; re-plumbing eth0 under a
// running k3s is deliberately not attempted. Already-pinned nodes pass
// untouched.
func pinNodeIP(ctx context.Context) error {
	raw, err := inspectNodeRaw(ctx)
	if err != nil {
		return err
	}
	endpoint, err := parseNodeEndpoint(raw, networkName())
	if err != nil {
		return err
	}
	if endpoint.PinnedIP != "" {
		return nil
	}
	if endpoint.IP == "" {
		return fmt.Errorf("localdev: node container has no address on %s to pin", networkName())
	}
	if err := stopNode(ctx); err != nil {
		return err
	}
	if err := connectNodeStatic(ctx, endpoint.IP); err != nil {
		return err
	}
	return startNode(ctx)
}

// NodeHealth is what docker's Running bit cannot say: whether the
// apiserver answers, and the crash-loop diagnosis when it does not.
type NodeHealth struct {
	Ready        bool
	Restarting   bool
	RestartCount int
	NodeIPFatal  bool
	Detail       string
}

// DiagnoseNode combines one short apiserver readiness probe with a
// docker-side inspection of the node container. A nil client skips the
// probe; the docker state is then the best available truth on readiness.
func DiagnoseNode(ctx context.Context, client *kube.Client) NodeHealth {
	var health NodeHealth
	if client != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := client.APIServerReady(probeCtx)
		cancel()
		if err == nil {
			health.Ready = true
			return health
		}
		health.Detail = err.Error()
	}
	raw, err := inspectNodeRaw(ctx)
	if err != nil {
		if health.Detail == "" {
			health.Detail = err.Error()
		}
		return health
	}
	state, stateErr := parseNodeState(raw)
	if stateErr == nil {
		health.Restarting = state.Restarting
		health.RestartCount = state.RestartCount
	}
	if logs, err := nodeLogsTail(ctx, fatalScanLines); err == nil && hasNodeIPFatal(logs) {
		health.NodeIPFatal = true
		health.Detail = "k3s is crash-looping: the node IP moved away from its registered address"
	}
	if client == nil && stateErr == nil {
		health.Ready = state.Running && !state.Restarting && !health.NodeIPFatal
	}
	return health
}

// NodeUnhealthyError reports a node whose apiserver does not answer after
// the automatic repair was impossible or failed; recreating the platform
// is the remaining cure, and the caller owns offering it.
type NodeUnhealthyError struct {
	Cluster   string
	Diagnosis string
	Cause     error
}

func (e *NodeUnhealthyError) Error() string {
	return fmt.Sprintf("the %s node is unhealthy: %s; recreate the local platform "+
		"with `skali dev reset`, then run `skali dev` again", e.Cluster, e.Diagnosis)
}

func (e *NodeUnhealthyError) Unwrap() error { return e.Cause }

// repairNodeIP recovers the node IP k3s registered at creation, parks the
// crash loop, reattaches the node at that address (which doubles as the
// permanent pin against future drift), and waits the apiserver back.
func repairNodeIP(ctx context.Context, client *kube.Client, progress Progress) error {
	progress.Start("Repair node IP")
	progress.Note("k3s is crash-looping: the node IP moved away from its registered address")
	registered, err := registeredNodeIP(ctx)
	if err != nil || registered == "" {
		return &NodeUnhealthyError{Cluster: ClusterName(), Cause: err,
			Diagnosis: "its registered node IP could not be recovered from the docker log history"}
	}
	progress.Note("registered node IP " + registered)
	// A leftover tools helper may hold the registered address; anything
	// else holding it is not ours to evict.
	removeToolsNode(ctx)
	if raw, err := inspectNetworkRaw(ctx); err == nil {
		if spec, err := parseNetworkSpec(raw, nodeContainer()); err == nil {
			for name, address := range spec.Members {
				if address == registered {
					return &NodeUnhealthyError{Cluster: ClusterName(),
						Diagnosis: fmt.Sprintf("its registered address %s is held by container %s",
							registered, name)}
				}
			}
		}
	}
	// Stop first, always: it parks the flapping endpoint and settles the
	// restart-policy race (a manual stop cancels the loop atomically).
	if err := stopNode(ctx); err != nil {
		return &NodeUnhealthyError{Cluster: ClusterName(), Cause: err,
			Diagnosis: "its crash loop could not be stopped"}
	}
	if err := connectNodeStatic(ctx, registered); err != nil {
		// Best effort: leave the user no worse off than a crash loop.
		_ = startNode(ctx)
		return &NodeUnhealthyError{Cluster: ClusterName(), Cause: err,
			Diagnosis: fmt.Sprintf("it could not be reattached at %s", registered)}
	}
	if err := startNode(ctx); err != nil {
		return &NodeUnhealthyError{Cluster: ClusterName(), Cause: err,
			Diagnosis: "it did not start after the reattach"}
	}
	if err := waitNodeReady(ctx, client, 3*time.Minute, progress); err != nil {
		return err
	}
	// Pod sandboxes born during the crash loop can survive into the
	// repaired boot with wedged pod networking (observed as the cluster
	// DNS refusing connections, stalling the converge); one clean reboot
	// on the pinned address makes the kubelet and controllers replace
	// them.
	progress.Note("rebooting once more to replace crash-era pod sandboxes")
	if err := stopNode(ctx); err != nil {
		return &NodeUnhealthyError{Cluster: ClusterName(), Cause: err,
			Diagnosis: "it could not be stopped for the post-repair reboot"}
	}
	if err := startNode(ctx); err != nil {
		return &NodeUnhealthyError{Cluster: ClusterName(), Cause: err,
			Diagnosis: "it did not start after the post-repair reboot"}
	}
	if err := waitNodeReady(ctx, client, 3*time.Minute, progress); err != nil {
		return err
	}
	progress.Done("pinned " + registered)
	return nil
}

// waitNodeReady polls the apiserver probe on a freshly started node. The
// manual start reset the restart counter, so a climbing counter (or a
// mid-restart flap) is a renewed crash, not leftover history.
func waitNodeReady(ctx context.Context, client *kube.Client, window time.Duration, progress Progress) error {
	deadline := time.Now().Add(window)
	var lastErr error
	for tick := 0; ; tick++ {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		lastErr = client.APIServerReady(probeCtx)
		cancel()
		if lastErr == nil {
			return nil
		}
		if raw, err := inspectNodeRaw(ctx); err == nil {
			if state, err := parseNodeState(raw); err == nil && (state.Restarting || state.RestartCount > 0) {
				diagnosis := "k3s keeps crashing after the reattach"
				if logs, err := nodeLogsTail(ctx, fatalScanLines); err == nil && hasNodeIPFatal(logs) {
					diagnosis = "k3s still cannot find an interface for its node IP after the reattach"
				}
				return &NodeUnhealthyError{Cluster: ClusterName(), Cause: lastErr, Diagnosis: diagnosis}
			}
		}
		if time.Now().After(deadline) {
			return &NodeUnhealthyError{Cluster: ClusterName(), Cause: lastErr,
				Diagnosis: "its apiserver did not come back within " + window.String() + " of the repair"}
		}
		if tick > 0 && tick%10 == 0 {
			progress.Note("waiting for the kube apiserver")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ensureNodeReady proves the running node's apiserver answers before
// anything docker-execs into the container or applies to the cluster: a
// crash-looping node still counts as ClusterRunning, and without this
// gate the failure surfaces as an opaque mid-converge apply error. A
// booting node (the docker daemon itself may just have restarted) is
// waited out; the node-IP crash loop is repaired in place. The returned
// restarted reports a node reboot, which the caller treats like a fresh
// cluster start.
func ensureNodeReady(ctx context.Context, client *kube.Client, progress Progress) (restarted bool, err error) {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err = client.APIServerReady(probeCtx)
	cancel()
	if err == nil {
		return false, nil
	}
	if health := DiagnoseNode(ctx, nil); health.NodeIPFatal {
		return true, repairNodeIP(ctx, client, progress)
	}
	progress.Start("Wait for kube apiserver")
	deadline := time.Now().Add(90 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = client.APIServerReady(probeCtx)
		cancel()
		if err == nil {
			// The apiserver was down and came back: the node just booted,
			// so the caller's edge grace applies like a fresh start.
			progress.Done("answering")
			return true, nil
		}
		// The fatal often only shows after a boot attempt or two.
		if logs, tailErr := nodeLogsTail(ctx, fatalScanLines); tailErr == nil && hasNodeIPFatal(logs) {
			progress.Skip("node is crash-looping")
			return true, repairNodeIP(ctx, client, progress)
		}
		if time.Now().After(deadline) {
			health := DiagnoseNode(ctx, nil)
			return false, &NodeUnhealthyError{Cluster: ClusterName(), Cause: err,
				Diagnosis: fmt.Sprintf("its apiserver did not answer within 90 seconds "+
					"(%d restarts; %s)", health.RestartCount, err.Error())}
		}
	}
}
