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
	"strconv"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/utils"
)

// The pinned local topology. A K3sImage bump must carry k3sBuiltinImages
// (images.go) along to the new release's k3s-images.txt.
const (
	K3sImage   = "rancher/k3s:v1.36.3-k3s1"
	AdminEmail = "dev@skali.localhost"
)

// The cluster name and host loopback ports are constants of the product
// contract; the SKALI_DEV_* environment overrides exist so the end-to-end
// suite runs against a throwaway installation without touching a real one.

func ClusterName() string { return utils.EnvOr("SKALI_DEV_CLUSTER", "skali-dev") }

// HTTPPort() publishes the traefik edge; the local platform is HTTP-only
// by decision (TLS issuance is a production concern). RegistryPort()
// publishes the managed registry for host-side pushes.
func HTTPPort() int     { return envPortOr("SKALI_DEV_HTTP_PORT", 8080) }
func RegistryPort() int { return envPortOr("SKALI_DEV_REGISTRY_PORT", 5510) }

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
	ports := []int{HTTPPort(), RegistryPort()}
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

// MasterURL() reaches the in-cluster skalid through the edge.
func MasterURL() string { return fmt.Sprintf("http://skali.localhost:%d", HTTPPort()) }

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

// State is the local installation record; reset removes it.
type State struct {
	Cluster       string    `json:"cluster"`
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
}

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

func statePath() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "localdev.json"), nil
}

// KubeconfigPath is where the dev cluster's kubeconfig lives.
func KubeconfigPath() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kubeconfig"), nil
}

func LoadState() (*State, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
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
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("localdev: write state: %w", err)
	}
	return nil
}

// RemoveState deletes the local installation record and kubeconfig.
func RemoveState() error {
	path, err := statePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("localdev: remove state: %w", err)
	}
	if kubeconfig, err := KubeconfigPath(); err == nil {
		_ = os.Remove(kubeconfig)
	}
	return nil
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
		K3sImage:      K3sImage,
		SkalidImage:   skalidImage,
		AdminEmail:    AdminEmail,
		AdminPassword: password,
		AuthSecret:    authSecret,
		CreatedAt:     time.Now(),
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

func Status(ctx context.Context) (ClusterStatus, error) {
	out, err := exec.CommandContext(ctx, k3dBinary(), "cluster", "list", "-o", "json").Output()
	if err != nil {
		return ClusterAbsent, fmt.Errorf("localdev: k3d cluster list: %w", err)
	}
	var clusters []struct {
		Name  string `json:"name"`
		Nodes []struct {
			State struct {
				Running bool `json:"Running"`
			} `json:"State"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(out, &clusters); err != nil {
		return ClusterAbsent, fmt.Errorf("localdev: decode k3d cluster list: %w", err)
	}
	for _, cluster := range clusters {
		if cluster.Name != ClusterName() {
			continue
		}
		for _, node := range cluster.Nodes {
			if node.State.Running {
				return ClusterRunning, nil
			}
		}
		return ClusterStopped, nil
	}
	return ClusterAbsent, nil
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

// legacyLayout reports a cluster from before the single-container layout:
// its node still has k3d's generated name, with a serverlb next to it.
// The node cannot be renamed in place, because the load balancer reaches
// it by its docker DNS name; those clusters are recreated instead.
func legacyLayout(ctx context.Context) bool {
	return exec.CommandContext(ctx, "docker", "container", "inspect", "k3d-"+ClusterName()+"-server-0").Run() == nil
}

// HasLoopbackPortMaps reports whether the existing node container publishes
// the loopback service range. Port maps are create-time k3d options, so a
// cluster from before the range must be recreated (skali dev reset).
// Checking the last port of the range suffices: the maps are created as one
// block. Works on stopped containers too.
func HasLoopbackPortMaps(ctx context.Context) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "container", "inspect", nodeContainer()).Output()
	if err != nil {
		return false, fmt.Errorf("localdev: inspect node container: %w", err)
	}
	return portBindingsHaveLoopback(out, bundle.S3NodePort,
		LoopbackPortBase()+loopbackNodePortCount-1)
}

// portBindingsHaveLoopback checks a docker-inspect document for a published
// binding of the container port to the loopback host port.
func portBindingsHaveLoopback(raw []byte, nodePort, hostPort int) (bool, error) {
	var doc []struct {
		HostConfig struct {
			PortBindings map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
		} `json:"HostConfig"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("localdev: decode container inspect: %w", err)
	}
	if len(doc) == 0 {
		return false, fmt.Errorf("localdev: container inspect returned no entries")
	}
	for _, binding := range doc[0].HostConfig.PortBindings[fmt.Sprintf("%d/tcp", nodePort)] {
		if binding.HostPort == strconv.Itoa(hostPort) {
			return true, nil
		}
	}
	return false, nil
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
	dir, err := StateDir()
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
	args := []string{
		"cluster", "create", ClusterName(),
		"--image", K3sImage,
		"--no-lb",
		"--kubeconfig-update-default=false",
		"--kubeconfig-switch-context=false",
		"--registry-config", registries,
		"-p", fmt.Sprintf("127.0.0.1:%d:80@server:0:direct", HTTPPort()),
		"-p", fmt.Sprintf("127.0.0.1:%d:30500@server:0:direct", RegistryPort()),
	}
	args = append(args, loopbackPortArgs()...)
	args = append(args, "--wait")
	if out, err := exec.CommandContext(ctx, k3dBinary(), args...).CombinedOutput(); err != nil {
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
	if out, err := exec.CommandContext(ctx, k3dBinary(), "cluster", "start", ClusterName()).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: k3d cluster start: %w\n%s", err, out)
	}
	removeToolsNode(ctx)
	return WriteKubeconfig(ctx)
}

func Stop(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, k3dBinary(), "cluster", "stop", ClusterName()).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: k3d cluster stop: %w\n%s", err, out)
	}
	return nil
}

func Delete(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, k3dBinary(), "cluster", "delete", ClusterName()).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: k3d cluster delete: %w\n%s", err, out)
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

// ImportImages loads local docker images into the cluster's containerd,
// one image per call through k3d's proven tools-node path. Direct mode is
// deliberately not used: measured on k3d 5.9.0 it fails mid-stream yet
// exits zero. k3d's exit code is not trusted either way; every import is
// verified against the node's own image store and retried once, so a
// silent import failure surfaces here instead of as an ImagePullBackOff
// five minutes later.
func ImportImages(ctx context.Context, images ...string) error {
	for _, image := range images {
		if imageInCluster(ctx, image) {
			continue
		}
		if err := importImage(ctx, image); err != nil {
			return err
		}
		if imageInCluster(ctx, image) {
			continue
		}
		if err := importImage(ctx, image); err != nil {
			return err
		}
		if !imageInCluster(ctx, image) {
			return fmt.Errorf("localdev: image %s did not arrive in the cluster after import; "+
				"try `k3d image import -c %s %s` manually", image, ClusterName(), image)
		}
	}
	return nil
}

// importImage moves one host-daemon image into the node's containerd. The
// primary path exports a single-platform tar and streams it into the
// node's ctr: Docker's containerd store keeps pulled images as multi-arch
// indexes whose full docker-save tars reference never-pulled platform
// manifests, which the node's ctr rejects ("content digest not found") and
// k3d then reports as success anyway (measured on k3d 5.9.0). Hosts
// without the containerd store (no --platform on save) fall back to k3d's
// import, which handles their classic tars fine.
func importImage(ctx context.Context, image string) error {
	node := nodeContainer()
	if platform, err := hostPlatform(ctx); err == nil {
		if err := streamImage(ctx, node, platform, image); err == nil {
			return nil
		}
	}
	if out, err := exec.CommandContext(ctx, k3dBinary(), "image", "import", "-c", ClusterName(), image).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: k3d image import %s: %w\n%s", image, err, out)
	}
	return nil
}

// streamImage pipes a single-platform docker save straight into the node's
// containerd, no tools node and no tar on disk.
func streamImage(ctx context.Context, node, platform, image string) error {
	save := exec.CommandContext(ctx, "docker", "image", "save", "--platform", platform, image)
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
		return fmt.Errorf("localdev: docker image save %s: %w\n%s", image, saveResult, saveErr.String())
	}
	if loadResult != nil {
		return fmt.Errorf("localdev: import %s into %s: %w\n%s", image, node, loadResult, loadErr.String())
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
