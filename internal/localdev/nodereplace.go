package localdev

// A k3d node cannot change its image, so a k3s upgrade replaces the node
// container around its data. Everything k3s keeps lives in the volumes the
// rancher/k3s image declares (the datastore and certificates, containerd's
// images, local-path volume data, kubelet state) plus k3d's image volume;
// the replacement mounts the very same volumes. The rest of the container
// is k3d's configuration on top of the image: hostname (the node name),
// command, environment and labels (the cluster token and k3d's identity),
// privileges, tmpfs, the pinned network address and alias, the host port
// mappings, and a few files k3d and k3s wrote into the root filesystem
// (k3d's entrypoint scripts, the registries config, the node password k3s
// registered the node under).
//
// The replacement is resumable from disk: the old container's spec and
// files are saved before anything changes, the new container is created
// under a staging name and proven to match the spec before the old one is
// removed (never with its volumes), and only then takes the node's name.
// The new k3s first boots after all of that, so up to the rename nothing
// has touched the data and the old node can still be started as it was.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// nodeSpec is the node container's configuration beyond its image.
type nodeSpec struct {
	Image      string            `json:"image"`
	Hostname   string            `json:"hostname"`
	Entrypoint []string          `json:"entrypoint,omitempty"` // nil inherits the image's
	Cmd        []string          `json:"cmd,omitempty"`        // nil inherits the image's
	Env        []string          `json:"env,omitempty"`        // beyond the image's, sorted
	Labels     map[string]string `json:"labels,omitempty"`     // beyond the image's
	Privileged bool              `json:"privileged"`
	Init       bool              `json:"init"`
	Restart    string            `json:"restart,omitempty"`
	Tmpfs      map[string]string `json:"tmpfs,omitempty"`
	Security   []string          `json:"security_opt,omitempty"`
	Cgroupns   string            `json:"cgroupns,omitempty"`
	ExtraHosts []string          `json:"extra_hosts,omitempty"`
	Ports      []string          `json:"ports"`  // docker -p values, sorted
	Mounts     []nodeMount       `json:"mounts"` // sorted by destination
	Network    string            `json:"network"`
	IP         string            `json:"ip"`
	Aliases    []string          `json:"aliases,omitempty"`
	// Files are root-filesystem paths carried over, each saved as the tar
	// docker cp produces.
	Files []nodeFile `json:"files,omitempty"`
}

type nodeMount struct {
	Type        string `json:"type"`   // volume or bind
	Source      string `json:"source"` // volume name or host path
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"read_only,omitempty"`
}

type nodeFile struct {
	Path    string `json:"path"`
	Archive string `json:"archive"` // file name inside the spec directory
}

// containerInspect is the part of docker container inspect the spec reads.
type containerInspect struct {
	ID     string `json:"Id"`
	Image  string `json:"Image"` // image ID
	Config struct {
		Hostname   string            `json:"Hostname"`
		Image      string            `json:"Image"`
		Env        []string          `json:"Env"`
		Cmd        []string          `json:"Cmd"`
		Entrypoint []string          `json:"Entrypoint"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		Privileged    bool  `json:"Privileged"`
		Init          *bool `json:"Init"`
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		Tmpfs        map[string]string `json:"Tmpfs"`
		SecurityOpt  []string          `json:"SecurityOpt"`
		CgroupnsMode string            `json:"CgroupnsMode"`
		ExtraHosts   []string          `json:"ExtraHosts"`
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress  string `json:"IPAddress"`
			IPAMConfig *struct {
				IPv4Address string `json:"IPv4Address"`
			} `json:"IPAMConfig"`
			Aliases []string `json:"Aliases"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

type imageInspect struct {
	Config struct {
		Env        []string          `json:"Env"`
		Cmd        []string          `json:"Cmd"`
		Entrypoint []string          `json:"Entrypoint"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`
}

func inspectContainer(ctx context.Context, name string) (*containerInspect, error) {
	out, err := exec.CommandContext(ctx, "docker", "container", "inspect", name).Output()
	if err != nil {
		return nil, fmt.Errorf("localdev: inspect container %s: %w", name, err)
	}
	var doc []containerInspect
	if err := json.Unmarshal(out, &doc); err != nil || len(doc) == 0 {
		return nil, fmt.Errorf("localdev: decode inspect of container %s: %v", name, err)
	}
	return &doc[0], nil
}

func inspectImage(ctx context.Context, image string) (*imageInspect, error) {
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", image).Output()
	if err != nil {
		return nil, fmt.Errorf("localdev: inspect image %s: %w", image, err)
	}
	var doc []imageInspect
	if err := json.Unmarshal(out, &doc); err != nil || len(doc) == 0 {
		return nil, fmt.Errorf("localdev: decode inspect of image %s: %v", image, err)
	}
	return &doc[0], nil
}

// containerExists reports a container by exact name.
func containerExists(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "container", "ls", "-a", "-q",
		"--filter", "name=^/"+name+"$").Output()
	if err != nil {
		return false, fmt.Errorf("localdev: list containers: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// specFromInspect derives the spec of a container: its configuration with
// the image's own defaults taken out, so a new image brings its own.
func specFromInspect(container *containerInspect, image *imageInspect) (*nodeSpec, error) {
	spec := &nodeSpec{
		Image:      container.Config.Image,
		Hostname:   container.Config.Hostname,
		Privileged: container.HostConfig.Privileged,
		Init:       container.HostConfig.Init != nil && *container.HostConfig.Init,
		Tmpfs:      container.HostConfig.Tmpfs,
		Security:   container.HostConfig.SecurityOpt,
		Cgroupns:   container.HostConfig.CgroupnsMode,
		ExtraHosts: container.HostConfig.ExtraHosts,
	}
	if !slices.Equal(container.Config.Entrypoint, image.Config.Entrypoint) {
		spec.Entrypoint = container.Config.Entrypoint
	}
	// docker resets the image's command once the entrypoint is overridden.
	if spec.Entrypoint != nil || !slices.Equal(container.Config.Cmd, image.Config.Cmd) {
		spec.Cmd = container.Config.Cmd
	}
	for _, entry := range container.Config.Env {
		if !slices.Contains(image.Config.Env, entry) {
			spec.Env = append(spec.Env, entry)
		}
	}
	slices.Sort(spec.Env)
	for key, value := range container.Config.Labels {
		if imageValue, ok := image.Config.Labels[key]; ok && imageValue == value {
			continue
		}
		if spec.Labels == nil {
			spec.Labels = map[string]string{}
		}
		spec.Labels[key] = value
	}
	if policy := container.HostConfig.RestartPolicy; policy.Name != "" && policy.Name != "no" {
		spec.Restart = policy.Name
		if policy.MaximumRetryCount > 0 {
			spec.Restart += ":" + strconv.Itoa(policy.MaximumRetryCount)
		}
	}
	for port, bindings := range container.HostConfig.PortBindings {
		for _, binding := range bindings {
			value := binding.HostPort + ":" + port
			if binding.HostIP != "" {
				value = binding.HostIP + ":" + value
			}
			spec.Ports = append(spec.Ports, value)
		}
	}
	slices.Sort(spec.Ports)
	for _, mount := range container.Mounts {
		switch mount.Type {
		case "volume":
			spec.Mounts = append(spec.Mounts, nodeMount{Type: "volume", Source: mount.Name, Destination: mount.Destination, ReadOnly: !mount.RW})
		case "bind":
			spec.Mounts = append(spec.Mounts, nodeMount{Type: "bind", Source: mount.Source, Destination: mount.Destination, ReadOnly: !mount.RW})
		default:
			return nil, fmt.Errorf("localdev: node mount %s is a %s mount, which the k3s upgrade cannot carry", mount.Destination, mount.Type)
		}
	}
	slices.SortFunc(spec.Mounts, func(a, b nodeMount) int { return strings.Compare(a.Destination, b.Destination) })
	if len(container.NetworkSettings.Networks) != 1 {
		return nil, fmt.Errorf("localdev: the node container is attached to %d networks; the k3s upgrade carries exactly one",
			len(container.NetworkSettings.Networks))
	}
	for name, network := range container.NetworkSettings.Networks {
		spec.Network = name
		// Older engines list the short container ID among the aliases;
		// docker adds that one by itself.
		for _, alias := range network.Aliases {
			if len(alias) == 12 && strings.HasPrefix(container.ID, alias) {
				continue
			}
			spec.Aliases = append(spec.Aliases, alias)
		}
		spec.IP = network.IPAddress
		if network.IPAMConfig != nil && network.IPAMConfig.IPv4Address != "" {
			spec.IP = network.IPAMConfig.IPv4Address
		}
	}
	return spec, nil
}

// createArgs renders the docker create invocation of a container with this
// spec on image.
func (spec *nodeSpec) createArgs(name, image string) []string {
	args := []string{"container", "create", "--name", name, "--hostname", spec.Hostname}
	command := spec.Cmd
	if len(spec.Entrypoint) > 0 {
		// The executed argv is entrypoint ++ cmd; the flag takes one word.
		args = append(args, "--entrypoint", spec.Entrypoint[0])
		command = append(slices.Clone(spec.Entrypoint[1:]), spec.Cmd...)
	}
	if spec.Privileged {
		args = append(args, "--privileged")
	}
	if spec.Init {
		args = append(args, "--init")
	}
	if spec.Restart != "" {
		args = append(args, "--restart", spec.Restart)
	}
	for _, mountPoint := range sortedKeys(spec.Tmpfs) {
		value := mountPoint
		if options := spec.Tmpfs[mountPoint]; options != "" {
			value += ":" + options
		}
		args = append(args, "--tmpfs", value)
	}
	for _, option := range spec.Security {
		args = append(args, "--security-opt", option)
	}
	if spec.Cgroupns != "" {
		args = append(args, "--cgroupns", spec.Cgroupns)
	}
	for _, host := range spec.ExtraHosts {
		args = append(args, "--add-host", host)
	}
	args = append(args, "--network", spec.Network, "--ip", spec.IP)
	for _, alias := range spec.Aliases {
		args = append(args, "--network-alias", alias)
	}
	for _, port := range spec.Ports {
		args = append(args, "-p", port)
	}
	for _, mount := range spec.Mounts {
		value := mount.Source + ":" + mount.Destination
		if mount.ReadOnly {
			value += ":ro"
		}
		args = append(args, "-v", value)
	}
	for _, entry := range spec.Env {
		args = append(args, "-e", entry)
	}
	for _, key := range sortedKeys(spec.Labels) {
		args = append(args, "--label", key+"="+spec.Labels[key])
	}
	args = append(args, image)
	return append(args, command...)
}

// sameNode compares the configuration two specs give their containers;
// the image and the carried files are the replacement's business.
func sameNode(a, b *nodeSpec) error {
	left, right := *a, *b
	left.Image, right.Image = "", ""
	left.Files, right.Files = nil, nil
	// A newer image may declare an extra volume; only the node's own must
	// all be there.
	for _, mount := range a.Mounts {
		if !slices.Contains(b.Mounts, mount) {
			return fmt.Errorf("mount %s:%s", mount.Source, mount.Destination)
		}
	}
	left.Mounts, right.Mounts = nil, nil
	if !reflect.DeepEqual(left, right) {
		wanted, _ := json.Marshal(left)
		got, _ := json.Marshal(right)
		return fmt.Errorf("wanted %s, got %s", wanted, got)
	}
	return nil
}

// carriedPaths lists the root-filesystem paths k3d and k3s wrote that the
// replacement needs, from docker diff (which works on stopped containers).
func carriedPaths(ctx context.Context, name string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "docker", "container", "diff", name).Output()
	if err != nil {
		return nil, fmt.Errorf("localdev: diff node container: %w", err)
	}
	return parseCarriedPaths(string(out)), nil
}

// parseCarriedPaths picks the carried paths out of docker diff output:
// k3d's scripts in /bin and the /etc/rancher tree.
func parseCarriedPaths(diff string) []string {
	var paths []string
	for line := range strings.Lines(diff) {
		kind, file, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || kind != "A" {
			continue
		}
		if file == "/etc/rancher" || path.Dir(file) == "/bin" && strings.HasPrefix(path.Base(file), "k3d-") {
			paths = append(paths, file)
		}
	}
	slices.Sort(paths)
	return paths
}

func nodeSpecDir() (string, error) {
	dir, err := ClusterDir(ClusterName())
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "node-upgrade"), nil
}

// saveNodeSpec captures the running or stopped node: its spec and its
// carried files, written into the spec directory before anything changes.
func saveNodeSpec(ctx context.Context) (*nodeSpec, error) {
	container, err := inspectContainer(ctx, nodeContainer())
	if err != nil {
		return nil, err
	}
	image, err := inspectImage(ctx, container.Image)
	if err != nil {
		return nil, err
	}
	spec, err := specFromInspect(container, image)
	if err != nil {
		return nil, err
	}
	if spec.IP == "" {
		// A stopped node from before the IP pin: the address k3s
		// registered is in its log history.
		if spec.IP, err = registeredNodeIP(ctx); err != nil {
			return nil, err
		}
		if spec.IP == "" {
			return nil, errors.New("localdev: the node's registered address is unknown; start the platform with skali dev start, then upgrade")
		}
	}
	dir, err := nodeSpecDir()
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	paths, err := carriedPaths(ctx, nodeContainer())
	if err != nil {
		return nil, err
	}
	for index, file := range paths {
		archive := fmt.Sprintf("file-%d.tar", index)
		target, err := os.OpenFile(filepath.Join(dir, archive), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, err
		}
		command := exec.CommandContext(ctx, "docker", "container", "cp", nodeContainer()+":"+file, "-")
		command.Stdout = target
		out, err := commandOutput(command)
		if closeErr := target.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, fmt.Errorf("localdev: save %s from the node: %w\n%s", file, err, out)
		}
		spec.Files = append(spec.Files, nodeFile{Path: file, Archive: archive})
	}
	raw, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, err
	}
	// The spec lands last: its presence means the capture is complete.
	if err := writeFileAtomic(filepath.Join(dir, "spec.json"), raw, 0o600); err != nil {
		return nil, err
	}
	return spec, nil
}

// loadNodeSpec reads a saved capture; nil when there is none.
func loadNodeSpec() (*nodeSpec, error) {
	dir, err := nodeSpecDir()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "spec.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var spec nodeSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("localdev: decode saved node spec: %w", err)
	}
	return &spec, nil
}

// removeNodeSpec drops the capture once the replaced node proved healthy.
func removeNodeSpec() error {
	dir, err := nodeSpecDir()
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// commandOutput runs a command whose stdout is already redirected and
// returns its stderr for the error.
func commandOutput(command *exec.Cmd) ([]byte, error) {
	var stderr strings.Builder
	command.Stderr = &stderr
	err := command.Run()
	return []byte(stderr.String()), err
}

// stageNode creates the replacement under name from the spec on image,
// restores the carried files, and proves the result matches the spec.
func stageNode(ctx context.Context, spec *nodeSpec, name, image string) error {
	for _, mount := range spec.Mounts {
		if mount.Type != "volume" {
			continue
		}
		if err := exec.CommandContext(ctx, "docker", "volume", "inspect", mount.Source).Run(); err != nil {
			return fmt.Errorf("localdev: the node's volume %s (%s) is gone; the platform's data cannot be carried over", mount.Source, mount.Destination)
		}
	}
	if out, err := exec.CommandContext(ctx, "docker", spec.createArgs(name, image)...).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: create the replacement node: %w\n%s", err, out)
	}
	dir, err := nodeSpecDir()
	if err != nil {
		return err
	}
	for _, file := range spec.Files {
		source, err := os.Open(filepath.Join(dir, file.Archive))
		if err != nil {
			return fmt.Errorf("localdev: read saved node file %s: %w", file.Path, err)
		}
		command := exec.CommandContext(ctx, "docker", "container", "cp", "-", name+":"+path.Dir(file.Path))
		command.Stdin = source
		out, err := commandOutput(command)
		source.Close()
		if err != nil {
			return fmt.Errorf("localdev: restore %s into the replacement node: %w\n%s", file.Path, err, out)
		}
	}
	container, err := inspectContainer(ctx, name)
	if err != nil {
		return err
	}
	imageDoc, err := inspectImage(ctx, container.Image)
	if err != nil {
		return err
	}
	staged, err := specFromInspect(container, imageDoc)
	if err != nil {
		return err
	}
	if staged.Image != image {
		return fmt.Errorf("localdev: the replacement node runs %s instead of %s", staged.Image, image)
	}
	if err := sameNode(spec, staged); err != nil {
		return fmt.Errorf("localdev: the replacement node does not match the node it replaces: %w", err)
	}
	return nil
}

// replaceNode moves the node container to image, keeping its volumes and
// configuration. Every step is safe to repeat: an interrupted run resumes
// from the saved capture and whichever of the two containers exists. The
// node is left stopped; the caller's start boots the new k3s.
func replaceNode(ctx context.Context, image string) error {
	node, staging := nodeContainer(), nodeContainer()+"-upgrade"
	nodeExists, err := containerExists(ctx, node)
	if err != nil {
		return err
	}
	stagingExists, err := containerExists(ctx, staging)
	if err != nil {
		return err
	}
	if nodeExists {
		current, err := inspectContainer(ctx, node)
		if err != nil {
			return err
		}
		if current.Config.Image == image {
			if stagingExists {
				return removeContainer(ctx, staging)
			}
			return nil
		}
	}
	spec, err := loadNodeSpec()
	if err != nil {
		return err
	}
	if spec == nil {
		if !nodeExists {
			return fmt.Errorf("localdev: the node container %s is missing and no saved capture exists to recreate it from", node)
		}
		if spec, err = saveNodeSpec(ctx); err != nil {
			return err
		}
	}
	switch {
	case nodeExists:
		// Rebuild the staging container from scratch: a leftover may be
		// half-restored. It never ran, so removing it loses nothing.
		if stagingExists {
			if err := removeContainer(ctx, staging); err != nil {
				return err
			}
		}
		if err := stopNodeContainer(ctx, node); err != nil {
			return err
		}
		if err := stageNode(ctx, spec, staging, image); err != nil {
			_ = removeContainer(ctx, staging)
			return err
		}
		// Without -v: the volumes are the platform's data, and the
		// staging container already holds them.
		if out, err := exec.CommandContext(ctx, "docker", "container", "rm", node).CombinedOutput(); err != nil {
			return fmt.Errorf("localdev: remove the old node container: %w\n%s", err, out)
		}
	case !stagingExists:
		// Both are gone (removed by hand): rebuild from the capture.
		if err := stageNode(ctx, spec, staging, image); err != nil {
			_ = removeContainer(ctx, staging)
			return err
		}
	}
	// Only a verified staging container survives the old node's removal.
	if out, err := exec.CommandContext(ctx, "docker", "container", "rename", staging, node).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: rename the replacement node: %w\n%s", err, out)
	}
	return nil
}

// stopNodeContainer stops the node with time for k3s to shut down cleanly
// before its datastore moves to the new binary.
func stopNodeContainer(ctx context.Context, name string) error {
	if out, err := exec.CommandContext(ctx, "docker", "container", "stop", "--time", "60", name).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: stop node container: %w\n%s", err, out)
	}
	return nil
}

// removeContainer deletes a container that never ran, without volumes.
func removeContainer(ctx context.Context, name string) error {
	if out, err := exec.CommandContext(ctx, "docker", "container", "rm", "-f", name).CombinedOutput(); err != nil {
		return fmt.Errorf("localdev: remove container %s: %w\n%s", name, err, out)
	}
	return nil
}
