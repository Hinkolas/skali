package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"strconv"
	"strings"
	"time"
)

// limaAbsentExit signals a missing path from the in-guest shell snippets.
// It is outside the exit codes the wrapped tools use, so it never collides
// with a real failure.
const limaAbsentExit = 44

// Lima runs every operation inside one Lima managed VM by invoking limactl
// on this machine. The engine sees the VM as its target host: every command
// runs under sudo inside the guest, so the engine's root and Linux probes
// pass untouched. Callers must ensure the instance is running before
// handing Lima to the engine: limactl reports a missing or stopped instance
// as exit 1, which is indistinguishable from a remote command failing with
// exit 1.
type Lima struct {
	Instance string
	// Host executes limactl itself; nil selects Local. Tests inject a Fake
	// to record and script the limactl invocations.
	Host Runner
}

var _ APIAddresser = Lima{}

func (l Lima) host() Runner {
	if l.Host != nil {
		return l.Host
	}
	return Local{}
}

// shellArgs wraps a guest argv in `limactl shell`. The explicit workdir
// matters: the VM mounts nothing from this machine, so the current
// directory does not exist in the guest and limactl would warn on stderr.
func (l Lima) shellArgs(remote ...string) []string {
	return append([]string{"shell", "--workdir", "/", l.Instance, "--"}, remote...)
}

func (l Lima) Run(ctx context.Context, cmd Command) (Result, error) {
	remote := []string{"sudo"}
	if len(cmd.Env) > 0 {
		// sudo resets the environment and limactl does not forward it, so
		// the entries ride an explicit env prefix inside the guest.
		remote = append(remote, "env")
		remote = append(remote, cmd.Env...)
	}
	remote = append(remote, cmd.Name)
	remote = append(remote, cmd.Args...)
	return l.host().Run(ctx, Command{
		Name:   "limactl",
		Args:   l.shellArgs(remote...),
		Stdin:  cmd.Stdin,
		Stdout: cmd.Stdout,
		Stderr: cmd.Stderr,
	})
}

// The file operations pass the path as a positional shell argument, never
// interpolated into the script, so any path spelling is safe.

func (l Lima) ReadFile(ctx context.Context, path string) ([]byte, error) {
	result, err := l.host().Run(ctx, Command{
		Name: "limactl",
		Args: l.shellArgs("sudo", "sh", "-c",
			`[ -e "$1" ] || exit 44; cat -- "$1"`, "sh", path),
	})
	if err != nil {
		return nil, err
	}
	switch result.ExitCode {
	case 0:
		return []byte(result.Stdout), nil
	case limaAbsentExit:
		return nil, &fs.PathError{Op: "read", Path: path, Err: fs.ErrNotExist}
	default:
		return nil, fmt.Errorf("read %s in VM %s: exit %d: %s",
			path, l.Instance, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
}

func (l Lima) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	// The unconditional chmod mirrors Local.WriteFile: write permissions do
	// not apply to a pre-existing file.
	script := fmt.Sprintf(`cat > "$1" && chmod %o "$1"`, perm.Perm())
	result, err := l.host().Run(ctx, Command{
		Name:  "limactl",
		Args:  l.shellArgs("sudo", "sh", "-c", script, "sh", path),
		Stdin: bytes.NewReader(data),
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("write %s in VM %s: exit %d: %s",
			path, l.Instance, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func (l Lima) ReplaceFile(ctx context.Context, path, backup string, data []byte, perm fs.FileMode) error {
	script := fmt.Sprintf(`set -eu
target="$1"
backup="$2"
dir=$(dirname -- "$target")
base=$(basename -- "$target")
tmp=$(mktemp "$dir/.$base.tmp.XXXXXX")
trap 'rm -f -- "$tmp"' EXIT
cat > "$tmp"
chmod %o "$tmp"
sync "$tmp"
if [ -n "$backup" ] && [ -f "$target" ]; then
  btmp=$(mktemp "$dir/.$base.backup.XXXXXX")
  trap 'rm -f -- "$tmp" "$btmp"' EXIT
  cp -- "$target" "$btmp"
  chmod %o "$btmp"
  sync "$btmp"
  mv -f -- "$btmp" "$backup"
fi
mv -f -- "$tmp" "$target"
sync "$dir"
`, perm.Perm(), perm.Perm())
	result, err := l.host().Run(ctx, Command{
		Name:  "limactl",
		Args:  l.shellArgs("sudo", "sh", "-c", script, "sh", path, backup),
		Stdin: bytes.NewReader(data),
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("replace %s in VM %s: exit %d: %s",
			path, l.Instance, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func (l Lima) MkdirAll(ctx context.Context, path string, perm fs.FileMode) error {
	// Only the final directory gets the explicit mode; every directory the
	// engine creates has an existing parent, so this matches Local.
	script := fmt.Sprintf(`mkdir -p -- "$1" && chmod %o "$1"`, perm.Perm())
	result, err := l.host().Run(ctx, Command{
		Name: "limactl",
		Args: l.shellArgs("sudo", "sh", "-c", script, "sh", path),
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("mkdir %s in VM %s: exit %d: %s",
			path, l.Instance, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func (l Lima) Remove(ctx context.Context, path string) error {
	result, err := l.host().Run(ctx, Command{
		Name: "limactl",
		Args: l.shellArgs("sudo", "rm", "-rf", "--", path),
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("remove %s in VM %s: exit %d: %s",
			path, l.Instance, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func (l Lima) Stat(ctx context.Context, path string) (Info, error) {
	result, err := l.host().Run(ctx, Command{
		Name: "limactl",
		Args: l.shellArgs("sudo", "sh", "-c",
			`[ -e "$1" ] || exit 44; stat -c "%f %s" -- "$1"`, "sh", path),
	})
	if err != nil {
		return Info{}, err
	}
	switch result.ExitCode {
	case 0:
	case limaAbsentExit:
		return Info{}, nil
	default:
		return Info{}, fmt.Errorf("stat %s in VM %s: exit %d: %s",
			path, l.Instance, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	fields := strings.Fields(strings.TrimSpace(result.Stdout))
	if len(fields) != 2 {
		return Info{}, fmt.Errorf("stat %s in VM %s: unexpected output %q",
			path, l.Instance, strings.TrimSpace(result.Stdout))
	}
	raw, err := strconv.ParseUint(fields[0], 16, 32)
	if err != nil {
		return Info{}, fmt.Errorf("stat %s in VM %s: parse mode %q: %w", path, l.Instance, fields[0], err)
	}
	size, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return Info{}, fmt.Errorf("stat %s in VM %s: parse size %q: %w", path, l.Instance, fields[1], err)
	}
	// Permission bits plus the directory bit (S_IFDIR) cover every engine
	// consumer; finer type fidelity is not needed.
	mode := fs.FileMode(raw & 0o777)
	if raw&0x4000 != 0 {
		mode |= fs.ModeDir
	}
	return Info{Exists: true, Mode: mode, Size: size}, nil
}

func (l Lima) ProbeHTTP(ctx context.Context, request HTTPRequest) (HTTPResponse, error) {
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	args := []string{"--silent", "--show-error", "--max-time",
		strconv.Itoa(max(1, int(timeout.Seconds()))), "--output", "-", "--write-out", "\n%{http_code}"}
	if request.Insecure {
		args = append(args, "--insecure")
	}
	if !request.Insecure && len(request.CACerts) > 0 {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return HTTPResponse{}, err
		}
		caPath := "/tmp/skali-preflight-ca-" + fmt.Sprintf("%x", random)
		if err := l.WriteFile(ctx, caPath, request.CACerts, 0o600); err != nil {
			return HTTPResponse{}, err
		}
		defer l.Remove(context.WithoutCancel(ctx), caPath)
		args = append(args, "--cacert", caPath)
	}

	var config strings.Builder
	switch {
	case request.Bearer != "":
		fmt.Fprintf(&config, "header = \"Authorization: Bearer %s\"\n", curlConfigEscape(request.Bearer))
	case request.Username != "":
		credentials := base64.StdEncoding.EncodeToString([]byte(request.Username + ":" + request.Password))
		fmt.Fprintf(&config, "header = \"Authorization: Basic %s\"\n", credentials)
	}
	if config.Len() > 0 {
		args = append(args, "--config", "-")
	}
	args = append(args, request.URL)
	result, err := l.Run(ctx, Command{
		Name:  "curl",
		Args:  args,
		Stdin: strings.NewReader(config.String()),
	})
	if err != nil {
		return HTTPResponse{}, err
	}
	if result.ExitCode != 0 {
		return HTTPResponse{}, fmt.Errorf("curl failed with exit code %d: %s",
			result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	index := strings.LastIndex(result.Stdout, "\n")
	if index < 0 {
		return HTTPResponse{}, fmt.Errorf("curl returned no HTTP status")
	}
	code, err := strconv.Atoi(strings.TrimSpace(result.Stdout[index+1:]))
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("parse curl HTTP status %q: %w", result.Stdout[index+1:], err)
	}
	return HTTPResponse{StatusCode: code, Body: []byte(result.Stdout[:index])}, nil
}

func curlConfigEscape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

// limaInstance is the slice of `limactl list --format json` output this
// package consumes; unknown fields are ignored.
type limaInstance struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Network []struct {
		Lima string `json:"lima"`
	} `json:"network"`
	Config struct {
		Networks []struct {
			Lima string `json:"lima"`
		} `json:"networks"`
		PortForwards []struct {
			GuestPort int `json:"guestPort"`
			HostPort  int `json:"hostPort"`
		} `json:"portForwards"`
	} `json:"config"`
}

func (i limaInstance) network() string {
	if len(i.Network) > 0 && i.Network[0].Lima != "" {
		return i.Network[0].Lima
	}
	if len(i.Config.Networks) > 0 {
		return i.Config.Networks[0].Lima
	}
	return ""
}

// APIAddress reports where this machine reaches the guest's Kubernetes API
// server. On a vmnet network (bridged, shared) the guest address itself is
// reachable; on user-v2 the Mac cannot reach guest addresses, but Lima
// forwards guest listeners to host loopback, honoring an explicit
// portForwards rule for guest port 6443 when the template declares one.
func (l Lima) APIAddress(ctx context.Context) (string, error) {
	result, err := l.host().Run(ctx, Command{
		Name: "limactl",
		Args: []string{"list", "--format", "json", l.Instance},
	})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("limactl list %s: exit %d: %s",
			l.Instance, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	var instance limaInstance
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &instance); err != nil {
		return "", fmt.Errorf("parse limactl list output for VM %s: %w", l.Instance, err)
	}

	network := instance.network()
	if network == "" || network == "user-v2" {
		port := 6443
		for _, forward := range instance.Config.PortForwards {
			if forward.GuestPort == 6443 && forward.HostPort != 0 {
				port = forward.HostPort
				break
			}
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
	}

	// vmnet NICs attach as lima0 inside the guest; user-v2 is the mode that
	// replaces eth0 instead.
	probe, err := l.Run(ctx, Command{
		Name: "ip", Args: []string{"-4", "-o", "addr", "show", "dev", "lima0"},
	})
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(probe.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[2] != "inet" {
			continue
		}
		address, _, ok := strings.Cut(fields[3], "/")
		if ok && net.ParseIP(address) != nil {
			return net.JoinHostPort(address, "6443"), nil
		}
	}
	return "", fmt.Errorf("no IPv4 address on interface lima0 in VM %s; the VM network may still be acquiring a DHCP lease", l.Instance)
}
