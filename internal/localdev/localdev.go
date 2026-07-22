// Package localdev owns the disposable local development installation:
// prerequisite checks, the k3d cluster lifecycle (create, start, stop,
// delete), the local state record, and the bootstrap orchestration that
// applies the skali-system bundle. Its authority is deliberately limited
// to user-owned development installations (section 11.6); production
// clusters belong to `skali cluster`.
package localdev

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The pinned local topology (section 11.2 and the cli-dev transcript).
const (
	K3sImage   = "rancher/k3s:v1.33.3-k3s1"
	AdminEmail = "dev@skali.localhost"
)

// The cluster name and host loopback ports are constants of the product
// contract; the SKALI_DEV_* environment overrides exist so the end-to-end
// suite runs against a throwaway installation without touching a real one.

func ClusterName() string { return envOr("SKALI_DEV_CLUSTER", "skali-dev") }

// HTTPPort() publishes the traefik edge; the local platform is HTTP-only
// by decision (TLS issuance is a production concern). RegistryPort()
// publishes the managed registry for host-side pushes.
func HTTPPort() int     { return envPortOr("SKALI_DEV_HTTP_PORT", 8080) }
func RegistryPort() int { return envPortOr("SKALI_DEV_REGISTRY_PORT", 5510) }

// RegistryHost() names the registry in artifact references; valid from the
// host (buildx push through the port mapping) and from containerd (the
// registries.yaml mirror below).
func RegistryHost() string { return fmt.Sprintf("localhost:%d", RegistryPort()) }

// MasterURL() reaches the in-cluster skalid through the edge.
func MasterURL() string { return fmt.Sprintf("http://skali.localhost:%d", HTTPPort()) }

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
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
	authSecret, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	password, err := randomToken(18)
	if err != nil {
		return nil, err
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

func randomToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("localdev: generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// --- prerequisites ---

// CheckPrerequisites probes docker (with buildx) and k3d, returning
// actionable errors.
func CheckPrerequisites(ctx context.Context) (docker, k3d string, err error) {
	dockerOut, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		return "", "", errors.New("docker is required for skali dev and is not running: " +
			"install and start Docker, https://docs.docker.com/get-docker/")
	}
	docker = strings.TrimSpace(string(dockerOut))
	if _, err := exec.CommandContext(ctx, "docker", "buildx", "version").Output(); err != nil {
		return docker, "", errors.New("docker buildx is required for local builds: " +
			"install the buildx plugin, https://docs.docker.com/build/")
	}
	k3dOut, err := exec.CommandContext(ctx, "k3d", "version").Output()
	if err != nil {
		return docker, "", errors.New("k3d is required for skali dev: install k3d >= 5.6, https://k3d.io")
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(k3dOut)), "\n") {
		if version, found := strings.CutPrefix(line, "k3d version"); found {
			k3d = strings.TrimSpace(version)
		}
	}
	return docker, k3d, nil
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
	out, err := exec.CommandContext(ctx, "k3d", "cluster", "list", "-o", "json").Output()
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
		"--kubeconfig-update-default=false",
		"--kubeconfig-switch-context=false",
		"--registry-config", registries,
		"-p", fmt.Sprintf("127.0.0.1:%d:80@loadbalancer", HTTPPort()),
		"-p", fmt.Sprintf("127.0.0.1:%d:30500@server:0", RegistryPort()),
		"--wait",
	}
	if out, err := exec.CommandContext(ctx, "k3d", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: k3d cluster create: %w\n%s", err, out)
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
	out, err := exec.CommandContext(ctx, "k3d", "kubeconfig", "get", ClusterName()).Output()
	if err != nil {
		return fmt.Errorf("localdev: k3d kubeconfig get: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("localdev: write kubeconfig: %w", err)
	}
	return nil
}

func Start(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "k3d", "cluster", "start", ClusterName()).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: k3d cluster start: %w\n%s", err, out)
	}
	return WriteKubeconfig(ctx)
}

func Stop(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "k3d", "cluster", "stop", ClusterName()).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: k3d cluster stop: %w\n%s", err, out)
	}
	return nil
}

func Delete(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "k3d", "cluster", "delete", ClusterName()).CombinedOutput(); err != nil {
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

// ImportImage loads a local docker image into the cluster's containerd.
func ImportImage(ctx context.Context, image string) error {
	if out, err := exec.CommandContext(ctx, "k3d", "image", "import", "-c", ClusterName(), image).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: k3d image import %s: %w\n%s", image, err, out)
	}
	return nil
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
