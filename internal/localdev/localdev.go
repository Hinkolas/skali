// Package localdev owns the disposable local development installation:
// prerequisite checks, the k3d cluster lifecycle (create, start, stop,
// delete), the local state record, and the bootstrap orchestration that
// applies the skali-system bundle. Its authority is deliberately limited
// to user-owned development installations; production
// clusters belong to `skali cluster`.
package localdev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/filelock"
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/version"
)

// The pinned local topology. A K3sImage bump must carry k3sBuiltinImages
// (images.go) along to the new release's k3s-images.txt.
const (
	K3sImage   = "rancher/k3s:v1.36.3-k3s1"
	AdminEmail = "dev@skali.localhost"
)

// The cluster names and host loopback ports are constants of the product
// contract; the SKALI_DEV_* environment overrides exist so the end-to-end
// suite runs against a throwaway installation without touching a real one.

// PlatformVersion is the release this binary's local platform runs: the
// binary's own release, or empty for a development build, whose platform
// is the working tree (skalid:dev).
func PlatformVersion() string {
	if version.IsRelease(version.Version) {
		return version.Version
	}
	return ""
}

// The development installation has one durable identity, independent of the
// selected release. Test overrides must use an isolated state directory too.
func ClusterName() string { return utils.EnvOr("SKALI_DEV_CLUSTER", "skali-dev") }

// HTTPPort() and HTTPSPort() publish the traefik edge on the default web
// ports, so a route's local origin (https://<domain>.localhost) is exactly
// the origin a cluster would serve it on and browsers need no port. The
// overrides exist for the end-to-end suite: on any other ports the https
// redirect (which carries no port) and every origin an application derives
// from its domain stop matching. RegistryPort() publishes the managed
// registry for host-side pushes.
func HTTPPort() int     { return envPortOr("SKALI_DEV_HTTP_PORT", 80) }
func HTTPSPort() int    { return envPortOr("SKALI_DEV_HTTPS_PORT", 443) }
func RegistryPort() int { return envPortOr("SKALI_DEV_REGISTRY_PORT", 5510) }

// HostPortSuffix renders the ":port" a URL needs when port is not the
// scheme's default, and nothing otherwise.
func HostPortSuffix(port, defaultPort int) string {
	if port == defaultPort {
		return ""
	}
	return ":" + strconv.Itoa(port)
}

// LoopbackPortBase() is the first host port of the loopback service range:
// ten consecutive 127.0.0.1 ports mapped onto the substrate's fixed
// NodePorts (postgres pools 30501-30509, then S3 on 30510). The default is
// the identity mapping, so host port == NodePort and the server can
// compute host addresses without a translation table; the override shifts
// the whole range for the e2e suite.
func LoopbackPortBase() int {
	return envPortOr("SKALI_DEV_LOOPBACK_PORT_BASE", bundle.PoolNodePortMin)
}

// loopbackNodePortCount spans pools plus the S3 gateway.
const loopbackNodePortCount = bundle.S3NodePort - bundle.PoolNodePortMin + 1

// loopbackPortArgs renders the k3d -p mappings of the loopback service
// range.
func loopbackPortArgs() []string {
	base := LoopbackPortBase()
	args := make([]string, 0, 2*loopbackNodePortCount)
	for offset := range loopbackNodePortCount {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d@server:0:direct",
			base+offset, bundle.PoolNodePortMin+offset))
	}
	return args
}

// ReservedHostPorts lists the host ports the local platform occupies or
// maps even while idle: the edge, the registry, and the loopback service
// range k3d publishes at cluster create time. Dev port allocation must
// never hand these out, because a stopped cluster leaves them bindable.
func ReservedHostPorts() []int {
	ports := []int{HTTPPort(), HTTPSPort(), RegistryPort()}
	base := LoopbackPortBase()
	for offset := range loopbackNodePortCount {
		ports = append(ports, base+offset)
	}
	return ports
}

// RegistryHost() names the registry in artifact references; valid from the
// host (buildx push through the port mapping) and from containerd (the
// registries.yaml mirror below).
func RegistryHost() string { return fmt.Sprintf("localhost:%d", RegistryPort()) }

// MasterURL() reaches the in-cluster skalid through the TLS edge.
func MasterURL() string {
	return "https://" + bundle.LocalPlatformHost + HostPortSuffix(HTTPSPort(), 443)
}

func envPortOr(name string, fallback int) int {
	if value := os.Getenv(name); value != "" {
		var port int
		if _, err := fmt.Sscanf(value, "%d", &port); err == nil && port > 0 {
			return port
		}
	}
	return fallback
}

var ErrNotInstalled = errors.New("localdev: the local platform is not installed")

// State is one local platform's installation record; reset removes it.
type State struct {
	Cluster string `json:"cluster"`
	// Version is the platform release this cluster runs; empty for the
	// working tree. Legacy records (before v0.1.0-rc.3) have none and
	// imply it through SkalidImage.
	Version       string    `json:"version,omitempty"`
	K3sImage      string    `json:"k3s_image"`
	SkalidImage   string    `json:"skalid_image"`
	AdminEmail    string    `json:"admin_email"`
	AdminPassword string    `json:"admin_password"`
	AuthSecret    string    `json:"auth_secret"`
	CreatedAt     time.Time `json:"created_at"`
	// ImportedImageID is the docker image ID last imported into the
	// cluster: the content identity behind the mutable SkalidImage tag.
	// While it matches the daemon's current ID the import is skipped.
	ImportedImageID string `json:"imported_image_id,omitempty"`
	// ImportedImages records the public platform images already imported
	// into the cluster's containerd. Version-pinned tags never move, so
	// presence in the record is presence in the cluster while its node
	// volumes live.
	ImportedImages []string `json:"imported_images,omitempty"`
	// Edge records the host ports k3d published the edge on at cluster
	// creation. Port mappings are fixed for the cluster's life, so a
	// record whose ports differ from the CLI's (or that predates TLS on
	// the edge and has none) needs a reset. Nil on records written before
	// the edge moved to https on the default ports.
	Edge *EdgePorts `json:"edge,omitempty"`
	// CATrustAttempted remembers that skali dev already offered to install
	// the development CA into the trust store, so a declined password
	// dialog is not asked again on every run; skali dev trust retries.
	CATrustAttempted bool `json:"ca_trust_attempted,omitempty"`
}

// EdgePorts are the host ports the edge is published on.
type EdgePorts struct {
	HTTP  int `json:"http"`
	HTTPS int `json:"https"`
}

// currentEdgePorts are the ports this CLI publishes a new cluster on.
func currentEdgePorts() *EdgePorts { return &EdgePorts{HTTP: HTTPPort(), HTTPS: HTTPSPort()} }

// StateDir is $XDG_STATE_HOME/skali, defaulting to ~/.local/state/skali on
// every OS (the deliberate cliconfig convention).
func StateDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "skali"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("localdev: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "skali"), nil
}

// ClusterDir holds one cluster's record, kubeconfig, and registries
// config: <StateDir>/dev/<cluster>. Each platform release has its own.
func ClusterDir(name string) (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dev", name), nil
}

func clusterFile(name, file string) (string, error) {
	dir, err := ClusterDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, file), nil
}

func statePath() (string, error) { return clusterFile(ClusterName(), "state.json") }

// KubeconfigPath is where this platform's kubeconfig lives.
func KubeconfigPath() (string, error) { return clusterFile(ClusterName(), "kubeconfig") }

// The legacy record of releases before v0.1.0-rc.3: one record, one
// kubeconfig, and one registries config at the top of the state directory.
const (
	legacyStateFile      = "localdev.json"
	legacyKubeconfigFile = "kubeconfig"
	legacyRegistriesFile = "registries.yaml"
)

func LoadState() (*State, error) { return LoadStateFor(ClusterName()) }

// LoadStateFor reads the record of the named cluster; ErrNotInstalled when
// there is none.
func LoadStateFor(name string) (*State, error) {
	path, err := clusterFile(name, "state.json")
	if err != nil {
		return nil, err
	}
	return readState(path)
}

func readState(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotInstalled
		}
		return nil, fmt.Errorf("localdev: read state: %w", err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("localdev: decode state: %w", err)
	}
	return &state, nil
}

// Record identifies a recorded platform without exposing its credentials.
// Obsolete records are used only to produce explicit cleanup instructions.
type Record struct {
	Name        string
	Version     string // the platform release; empty for the working tree
	SkalidImage string
	K3sImage    string
	CreatedAt   time.Time
	// Legacy marks the shared cluster of releases before v0.1.0-rc.3,
	// recorded at the top of the state directory.
	Legacy bool
}

// Records lists every local platform this machine has a record of, sorted
// by name: the per-cluster records under dev/ and the legacy record when
// one exists.
func Records() ([]Record, error) {
	dir, err := StateDir()
	if err != nil {
		return nil, err
	}
	var records []Record
	entries, err := os.ReadDir(filepath.Join(dir, "dev"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("localdev: list platforms: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		state, err := LoadStateFor(entry.Name())
		if errors.Is(err, ErrNotInstalled) {
			continue
		}
		if err != nil {
			return nil, err
		}
		records = append(records, Record{
			Name:        entry.Name(),
			Version:     state.Version,
			SkalidImage: state.SkalidImage,
			K3sImage:    state.K3sImage,
			CreatedAt:   state.CreatedAt,
		})
	}
	legacy, err := readState(filepath.Join(dir, legacyStateFile))
	if err != nil && !errors.Is(err, ErrNotInstalled) {
		return nil, err
	}
	if legacy != nil {
		name := legacy.Cluster
		if name == "" {
			name = "skali-dev"
		}
		release, _ := version.PublishedSkalidVersion(legacy.SkalidImage)
		records = append(records, Record{
			Name:        name,
			Version:     release,
			SkalidImage: legacy.SkalidImage,
			K3sImage:    legacy.K3sImage,
			CreatedAt:   legacy.CreatedAt,
			Legacy:      true,
		})
	}
	slices.SortFunc(records, func(a, b Record) int { return strings.Compare(a.Name, b.Name) })
	return records, nil
}

// Lock serializes lifecycle operations. It lives outside the record directory
// so reset cannot unlink an inode another process is waiting to lock.
type lifecycleLockKey struct{}

// LockContext keeps a lifecycle operation and its login/cleanup in one lock.
func LockContext(ctx context.Context) (context.Context, func(), error) {
	unlock, err := Lock(ctx)
	return context.WithValue(ctx, lifecycleLockKey{}, true), unlock, err
}

func Lock(ctx context.Context) (func(), error) {
	if held, _ := ctx.Value(lifecycleLockKey{}).(bool); held {
		return func() {}, nil
	}
	dir, err := StateDir()
	if err != nil {
		return nil, err
	}
	return filelock.Acquire(ctx, filepath.Join(dir, "dev", "."+ClusterName()+".lock"))
}

// ObsoletePlatforms reports exact cleanup commands for records created by the
// abandoned per-release model. It never adopts, stops, or removes them.
func ObsoletePlatforms() error {
	records, err := Records()
	if err != nil {
		return err
	}
	var hints []string
	for _, record := range records {
		if record.Name == ClusterName() && !record.Legacy {
			continue
		}
		// Isolated test names are not another release of the user's platform.
		if !record.Legacy && !strings.HasPrefix(record.Name, "skali-dev-v") && record.Name != "skali-dev-working-tree" {
			continue
		}
		dir, _ := ClusterDir(record.Name)
		if record.Legacy {
			root, _ := StateDir()
			hints = append(hints, fmt.Sprintf("k3d cluster delete %s; then remove %s, %s, and %s", record.Name, filepath.Join(root, legacyStateFile), filepath.Join(root, legacyKubeconfigFile), filepath.Join(root, legacyRegistriesFile)))
			continue
		}
		hints = append(hints, fmt.Sprintf("k3d cluster delete %s; then remove %s", record.Name, dir))
	}
	if len(hints) > 0 {
		return fmt.Errorf("old local platform records need explicit cleanup (deletes their local data):\n  %s", strings.Join(hints, "\n  "))
	}
	return nil
}

// CheckVersion refuses all implicit release and substrate transitions.
func CheckVersion(state *State) error {
	if state == nil {
		return nil
	}
	if state.Version != PlatformVersion() || state.K3sImage != K3sImage {
		installed, wanted := state.Version, PlatformVersion()
		if installed == "" {
			installed = "working tree"
		}
		if wanted == "" {
			wanted = "working tree"
		}
		return fmt.Errorf("local dev platform runs %s (%s); selected CLI requires %s (%s); run skali dev reset to delete the local platform and its data, then skali dev to recreate it", installed, state.K3sImage, wanted, K3sImage)
	}
	_, published := version.PublishedSkalidVersion(state.SkalidImage)
	if state.Version == "" && published {
		return fmt.Errorf("working-tree platform record names a released skalid image; run skali dev reset")
	}
	if state.Version != "" && state.SkalidImage != version.PublishedSkalidImage(state.Version) {
		return fmt.Errorf("local platform image and recorded release disagree; run skali dev reset")
	}
	// k3d fixes the port mappings when it creates the cluster, so an edge
	// published elsewhere cannot be moved in place.
	wanted := currentEdgePorts()
	switch {
	case state.Edge == nil:
		return fmt.Errorf("local dev platform was created with a plain-HTTP edge on port 8080; this CLI serves https://*.localhost on ports %d and %d, and k3d port mappings are fixed at creation; run skali dev reset to delete the local platform and its data, then skali dev to recreate it", wanted.HTTP, wanted.HTTPS)
	case *state.Edge != *wanted:
		return fmt.Errorf("local dev platform publishes its edge on ports %d (http) and %d (https); this CLI expects %d and %d, and k3d port mappings are fixed at creation; run skali dev reset to delete the local platform and its data, then skali dev to recreate it", state.Edge.HTTP, state.Edge.HTTPS, wanted.HTTP, wanted.HTTPS)
	}
	return nil
}

func SaveState(state *State) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("localdev: create state directory: %w", err)
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("localdev: encode state: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}

	return nil
}

// RemoveState deletes this platform's record directory (record,
// kubeconfig, registries config).
func RemoveState() error {
	dir, err := ClusterDir(ClusterName())
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// NewState generates the secrets of a fresh installation.
func NewState(skalidImage string) (*State, error) {
	authSecret, err := utils.RandomToken(32)
	if err != nil {
		return nil, fmt.Errorf("localdev: generate secret: %w", err)
	}
	password, err := utils.RandomToken(18)
	if err != nil {
		return nil, fmt.Errorf("localdev: generate secret: %w", err)
	}
	return &State{
		Cluster:       ClusterName(),
		Version:       PlatformVersion(),
		K3sImage:      K3sImage,
		SkalidImage:   skalidImage,
		AdminEmail:    AdminEmail,
		AdminPassword: password,
		AuthSecret:    authSecret,
		CreatedAt:     time.Now(),
		Edge:          currentEdgePorts(),
	}, nil
}

// --- prerequisites ---

// CheckPrerequisites probes docker (with buildx) and a usable k3d,
// returning actionable errors. errK3dMissing is not one for the user:
// it asks Ensure to install the managed pinned k3d (k3d.go).
func CheckPrerequisites(ctx context.Context) (docker, k3d string, err error) {
	dockerOut, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		return "", "", errors.New(dockerInstallHint())
	}
	docker = strings.TrimSpace(string(dockerOut))
	if _, err := exec.CommandContext(ctx, "docker", "buildx", "version").Output(); err != nil {
		return docker, "", errors.New("docker buildx is required for local builds: " +
			"install the buildx plugin, https://docs.docker.com/build/")
	}
	k3d, err = checkK3d(ctx)
	if err != nil {
		return docker, "", err
	}
	return docker, k3d, nil
}

// dockerInstallHint names the docker gap; docker itself is never
// auto-installed (a desktop app the user must own and start), so the
// hint carries the concrete command where one exists.
func dockerInstallHint() string {
	if runtime.GOOS == "darwin" {
		return "docker is required for skali dev and is not running: " +
			"start Docker Desktop or OrbStack, or install one first " +
			"(brew install --cask docker), https://docs.docker.com/get-docker/"
	}
	return "docker is required for skali dev and is not running: " +
		"install and start Docker, https://docs.docker.com/get-docker/"
}

// --- k3d lifecycle (exec of the k3d binary; prerequisite checked) ---

// ClusterStatus reports the dev cluster's docker state.
type ClusterStatus string

const (
	ClusterAbsent  ClusterStatus = "absent"
	ClusterStopped ClusterStatus = "stopped"
	ClusterRunning ClusterStatus = "running"
)

// Status reports this platform's cluster.
func Status(ctx context.Context) (ClusterStatus, error) {
	statuses, err := Statuses(ctx)
	if err != nil {
		return ClusterAbsent, err
	}
	return StatusOf(statuses, ClusterName()), nil
}

// Statuses reports every k3d cluster on this machine in one k3d call,
// keyed by name.
func Statuses(ctx context.Context) (map[string]ClusterStatus, error) {
	out, err := exec.CommandContext(ctx, k3dBinary(), "cluster", "list", "-o", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("localdev: k3d cluster list: %w", err)
	}
	return parseClusterList(out)
}

// StatusOf looks a cluster up in a Statuses result; missing is absent.
func StatusOf(statuses map[string]ClusterStatus, name string) ClusterStatus {
	if status, ok := statuses[name]; ok {
		return status
	}
	return ClusterAbsent
}

func parseClusterList(raw []byte) (map[string]ClusterStatus, error) {
	var clusters []struct {
		Name  string `json:"name"`
		Nodes []struct {
			State struct {
				Running bool `json:"Running"`
			} `json:"State"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &clusters); err != nil {
		return nil, fmt.Errorf("localdev: decode k3d cluster list: %w", err)
	}
	statuses := make(map[string]ClusterStatus, len(clusters))
	for _, cluster := range clusters {
		statuses[cluster.Name] = ClusterStopped
		for _, node := range cluster.Nodes {
			if node.State.Running {
				statuses[cluster.Name] = ClusterRunning
				break
			}
		}
	}
	return statuses, nil
}

// nodeContainer is the docker name of the cluster's only node: the bare
// cluster name, so the docker surface shows one obviously named
// container. Create renames it from k3d's generated k3d-<cluster>-server-0;
// k3d itself finds nodes by docker labels, so its lifecycle commands keep
// working with the renamed container (verified on k3d 5.9.0).
func nodeContainer() string { return ClusterName() }

// removeToolsNode deletes the idle k3d-tools helper container that
// cluster create and start leave behind. It only exists to assist those
// commands; k3d recreates it on demand (image import) and cleans that
// one up itself.
func removeToolsNode(ctx context.Context) {
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", "k3d-"+ClusterName()+"-tools").Run()
}

// registriesConfig lets containerd on the nodes resolve the artifact
// reference host through the node-local NodePort.
func registriesConfig() string {
	return fmt.Sprintf(`mirrors:
  "%s":
    endpoint:
      - http://127.0.0.1:%d
`, RegistryHost(), 30500)
}

// Create provisions the pinned dev cluster with its port mappings and
// registry mirror, and writes the kubeconfig into the state directory.
// The cluster is one docker container named after itself: the single
// server needs no k3d load balancer (--no-lb, direct port mappings), and
// the node container drops its generated k3d name.
func Create(ctx context.Context) error {
	dir, err := ClusterDir(ClusterName())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("localdev: create state directory: %w", err)
	}
	registries := filepath.Join(dir, "registries.yaml")
	if err := os.WriteFile(registries, []byte(registriesConfig()), 0o600); err != nil {
		return fmt.Errorf("localdev: write registries config: %w", err)
	}
	if out, err := k3dRetryingBusyPorts(ctx, createArgs(registries)...); err != nil {
		return fmt.Errorf("localdev: k3d cluster create: %w\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "docker", "rename",
		"k3d-"+ClusterName()+"-server-0", nodeContainer()).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: rename node container: %w\n%s", err, out)
	}
	removeToolsNode(ctx)
	// The address docker allocated the node is an accident of creation
	// order (the tools helper above usually held the first free one), yet
	// k3s just registered it permanently; pin it as a static IPAM entry so
	// a docker daemon restart cannot move the node off its registration
	// and crash-loop k3s (see nodeip.go).
	if err := pinNodeIP(ctx); err != nil {
		return fmt.Errorf("localdev: pin node IP: %w", err)
	}
	return WriteKubeconfig(ctx)
}

// createArgs renders the k3d cluster create invocation: the pinned image,
// no load balancer, the registry mirror config, and the host port
// mappings (both edge entrypoints, the registry NodePort, and the loopback
// service range), all bound to the loopback address.
func createArgs(registries string) []string {
	args := []string{
		"cluster", "create", ClusterName(),
		"--image", K3sImage,
		"--no-lb",
		"--kubeconfig-update-default=false",
		"--kubeconfig-switch-context=false",
		"--registry-config", registries,
		"-p", fmt.Sprintf("127.0.0.1:%d:80@server:0:direct", HTTPPort()),
		"-p", fmt.Sprintf("127.0.0.1:%d:443@server:0:direct", HTTPSPort()),
		"-p", fmt.Sprintf("127.0.0.1:%d:30500@server:0:direct", RegistryPort()),
	}
	args = append(args, loopbackPortArgs()...)
	return append(args, "--wait")
}

// WriteKubeconfig refreshes the state-directory kubeconfig.
func WriteKubeconfig(ctx context.Context) error {
	path, err := KubeconfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("localdev: create state directory: %w", err)
	}
	out, err := exec.CommandContext(ctx, k3dBinary(), "kubeconfig", "get", ClusterName()).Output()
	if err != nil {
		return fmt.Errorf("localdev: k3d kubeconfig get: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("localdev: write kubeconfig: %w", err)
	}
	return nil
}

func Start(ctx context.Context) error {
	if out, err := k3dRetryingBusyPorts(ctx, "cluster", "start", ClusterName()); err != nil {
		return fmt.Errorf("localdev: k3d cluster start: %w\n%s", err, out)
	}
	removeToolsNode(ctx)
	return WriteKubeconfig(ctx)
}

// k3dRetryingBusyPorts runs a k3d command that binds the platform's host
// ports. A recently stopped cluster may have exited while Docker Desktop's
// port proxy still holds the bindings; it can
// release 127.0.0.1 bindings a moment later; a port still busy gets a
// short, bounded retry instead of a failure.
func k3dRetryingBusyPorts(ctx context.Context, args ...string) ([]byte, error) {
	const attempts = 3
	var out []byte
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		out, err = exec.CommandContext(ctx, k3dBinary(), args...).CombinedOutput()
		if err == nil || !portBusy(out) || attempt == attempts {
			return out, err
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return out, err
}

func portBusy(out []byte) bool {
	text := strings.ToLower(string(out))
	return strings.Contains(text, "port is already allocated") || strings.Contains(text, "address already in use")
}

func Stop(ctx context.Context) error { return StopFor(ctx, ClusterName()) }

// StopFor stops the named cluster; k3d returns once its container exited.
func StopFor(ctx context.Context, name string) error {
	if out, err := exec.CommandContext(ctx, k3dBinary(), "cluster", "stop", name).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: k3d cluster stop %s: %w\n%s", name, err, out)
	}
	return nil
}

func Delete(ctx context.Context) error { return DeleteFor(ctx, ClusterName()) }

// DeleteFor deletes the named cluster with its volumes.
func DeleteFor(ctx context.Context, name string) error {
	if out, err := exec.CommandContext(ctx, k3dBinary(), "cluster", "delete", name).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: k3d cluster delete %s: %w\n%s", name, err, out)
	}
	return nil
}

// ImageID resolves the docker image ID of a local image: its content
// identity, which changes exactly when the image was rebuilt.
func ImageID(ctx context.Context, image string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		return "", fmt.Errorf("localdev: image %s is not in the local docker daemon: "+
			"run task dev:image or pass --skalid-image", image)
	}
	return strings.TrimSpace(string(out)), nil
}

// ImportImages loads images into the cluster's containerd, one image per
// call, each verified against the node's own image store: the tools on the
// way do not report failure reliably (docker save on a containerd-store
// host emits a layerless tar and exits zero; k3d's import prints success
// over a failed ctr import), so presence in the node is the only truth,
// and a failure carries every underlying error instead of hiding behind a
// bare "did not arrive".
//
// The primary path exports the host daemon's copy and streams it into the
// node. Public images (the platform images and published skalid releases)
// have a second path, a pull inside the node: Docker's containerd store
// never downloads the blobs of layers another image already unpacked, so
// an image sharing a base with one pulled before it runs fine yet cannot
// be exported ("does not provide the specified platform"), and re-pulling
// does not repair it. The node's containerd then fetches the image itself,
// which is what k3s does for its built-ins anyway. Working-tree builds
// never take that path: their bits live only in the host daemon, and a
// registry may serve different ones under the same tag.
func ImportImages(ctx context.Context, images ...string) error {
	pullable := make(map[string]bool)
	for _, image := range RequiredImages() {
		pullable[image] = true
	}
	for _, image := range images {
		if imageInCluster(ctx, image) {
			continue
		}
		if _, published := version.PublishedSkalidVersion(image); published {
			pullable[image] = true
		}
		if err := importImage(ctx, image, pullable[image]); err != nil {
			return err
		}
	}
	return nil
}

// importImage moves one image into the node's containerd: the host daemon
// export streamed into the node's ctr first, a pull inside the node second
// when the image is public. Each path counts only once the node's image
// store holds the image.
func importImage(ctx context.Context, image string, pullable bool) error {
	var failures []error
	err := streamImage(ctx, image)
	if err == nil {
		if imageInCluster(ctx, image) {
			return nil
		}
		err = errors.New("the import reported success but the node does not hold the image")
	}
	failures = append(failures, fmt.Errorf("export from the host daemon: %w", err))
	if pullable {
		err := pullImageInNode(ctx, image)
		if err == nil {
			if imageInCluster(ctx, image) {
				return nil
			}
			err = errors.New("the pull reported success but the node does not hold the image")
		}
		failures = append(failures, fmt.Errorf("pull inside the node: %w", err))
	} else if unexportableHostImage(err) {
		failures = append(failures, errors.New("the host daemon holds the image but cannot export it "+
			"(Docker's containerd store skips layers another image already unpacked): "+
			"rebuild it, or docker load a complete copy"))
	}
	return fmt.Errorf("localdev: image %s did not arrive in the cluster:\n%w", image, errors.Join(failures...))
}

// unexportableHostImage recognizes the export failures of an image the host
// daemon holds without all of its layer blobs: docker save refusing the
// platform it cannot assemble, or exiting zero with a tar the node's ctr
// finds incomplete.
func unexportableHostImage(err error) bool {
	text := err.Error()
	return strings.Contains(text, "does not provide the specified platform") ||
		strings.Contains(text, "content digest") ||
		strings.Contains(text, "unrecognized image format")
}

// streamImage pipes a docker save straight into the node's containerd, no
// tools node and no tar on disk. Docker's containerd store keeps pulled
// images as multi-arch indexes whose full save tars reference never-pulled
// platform manifests, which the node's ctr rejects ("content digest not
// found"), so the export is narrowed to the node's platform. A daemon too
// old for the --platform flag has the classic store, whose tars are
// complete and single-platform, and is exported whole.
func streamImage(ctx context.Context, image string) error {
	platform, err := hostPlatform(ctx)
	if err != nil {
		platform = ""
	}
	err = streamImagePlatform(ctx, nodeContainer(), platform, image)
	if err != nil && platform != "" && lacksPlatformFlag(err) {
		err = streamImagePlatform(ctx, nodeContainer(), "", image)
	}
	return err
}

// lacksPlatformFlag recognizes a docker CLI that predates --platform on
// image save.
func lacksPlatformFlag(err error) bool {
	return strings.Contains(err.Error(), "unknown flag: --platform")
}

// streamImagePlatform runs one docker save (narrowed to platform when it
// is not empty) piped into the node's ctr import.
func streamImagePlatform(ctx context.Context, node, platform, image string) error {
	args := []string{"image", "save"}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	save := exec.CommandContext(ctx, "docker", append(args, image)...)
	load := exec.CommandContext(ctx, "docker", "exec", "-i", node, "ctr", "-n", "k8s.io", "images", "import", "-")
	pipe, err := save.StdoutPipe()
	if err != nil {
		return err
	}
	load.Stdin = pipe
	var saveErr, loadErr bytes.Buffer
	save.Stderr, load.Stderr = &saveErr, &loadErr
	if err := load.Start(); err != nil {
		return err
	}
	if err := save.Start(); err != nil {
		_ = load.Wait()
		return err
	}
	saveResult := save.Wait()
	loadResult := load.Wait()
	if saveResult != nil {
		return fmt.Errorf("docker image save %s: %w\n%s", image, saveResult, strings.TrimSpace(saveErr.String()))
	}
	if loadResult != nil {
		return fmt.Errorf("ctr import %s into %s: %w\n%s", image, node, loadResult, strings.TrimSpace(loadErr.String()))
	}
	return nil
}

// pullImageInNode has the node's containerd fetch a public image itself,
// through the node's own registry configuration.
func pullImageInNode(ctx context.Context, image string) error {
	out, err := exec.CommandContext(ctx, "docker", "exec", nodeContainer(), "crictl", "pull", image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("crictl pull %s: %w\n%s", image, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// hostPlatform is the docker server's os/arch, which is also every k3d
// node's platform.
func hostPlatform(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}").Output()
	if err != nil {
		return "", err
	}
	platform := strings.TrimSpace(string(out))
	if platform == "" || strings.Contains(platform, "<no value>") {
		return "", errors.New("localdev: docker server platform unavailable")
	}
	return platform, nil
}

// imageInCluster reports the image's presence in the server node's
// containerd, the ground truth the pods resolve against.
func imageInCluster(ctx context.Context, image string) bool {
	return exec.CommandContext(ctx, "docker", "exec", nodeContainer(), "crictl", "inspecti", "-q", image).Run() == nil
}

// BuildSkalidImage builds the control-plane image from the working tree;
// the developer path until published bootstrap images exist. Build output
// streams to output; nil falls back to stderr.
func BuildSkalidImage(ctx context.Context, repoRoot, tag string, output io.Writer) error {
	if output == nil {
		output = os.Stderr
	}
	args := []string{"build", "-t", tag, "-f", filepath.Join(repoRoot, "build", "skalid.Dockerfile"), repoRoot}
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("localdev: docker build skalid image: %w", err)
	}
	return nil
}
