// Package host abstracts privileged execution on one target host. The
// installer engine mutates hosts exclusively through Runner, so the same
// engine runs directly on a Linux host (the binary runs under sudo),
// against a scripted Fake in tests, and against a Lima-managed VM: Lima
// wraps every call in `limactl shell <vm> sudo`, which makes a
// macOS-managed VM a first-class install target rather than a test rig.
package host

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Command describes one process execution on the host.
type Command struct {
	Name string
	Args []string
	// Env entries (KEY=VALUE) are added to the host's environment.
	Env   []string
	Stdin io.Reader
	// Stdout and Stderr stream output when set; nil captures into Result.
	Stdout io.Writer
	Stderr io.Writer
}

// Result reports one finished execution. A nonzero ExitCode is data, not an
// error: state detection probes commands that are expected to fail.
type Result struct {
	ExitCode int
	// Stdout and Stderr hold captured output when the Command did not
	// stream it.
	Stdout string
	Stderr string
}

// Info reports what Stat learned about a path.
type Info struct {
	Exists bool
	Mode   fs.FileMode
	Size   int64
}

// HTTPRequest describes one read-only request made from the target host.
// Authentication stays out of command arguments and environment entries.
type HTTPRequest struct {
	URL      string
	CACerts  []byte
	Insecure bool
	Username string
	Password string
	Bearer   string
	Timeout  time.Duration
}

type HTTPResponse struct {
	StatusCode int
	Body       []byte
}

// Runner executes privileged operations on one target host. Run returns an
// error only when the command could not be executed at all; MkdirAll and
// Remove are idempotent.
type Runner interface {
	Run(ctx context.Context, cmd Command) (Result, error)
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
	// ReplaceFile durably replaces path and optionally preserves the
	// previous contents at backup.
	ReplaceFile(ctx context.Context, path, backup string, data []byte, perm fs.FileMode) error
	MkdirAll(ctx context.Context, path string, perm fs.FileMode) error
	// Remove deletes the path recursively; an absent path is not an error.
	Remove(ctx context.Context, path string) error
	Stat(ctx context.Context, path string) (Info, error)
	ProbeHTTP(ctx context.Context, request HTTPRequest) (HTTPResponse, error)
}

// APIAddresser is implemented by runners whose target host is not this
// machine. It reports the host:port on which this machine reaches the
// guest's Kubernetes API server. Local deliberately does not implement it,
// so the local kubeconfig is used byte for byte.
type APIAddresser interface {
	APIAddress(ctx context.Context) (string, error)
}

// Local runs directly on this host. It assumes the current process already
// holds the required privileges; the installer refuses to run unprivileged
// long before any mutation.
type Local struct{}

func (Local) Run(ctx context.Context, cmd Command) (Result, error) {
	execution := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	execution.Env = append(os.Environ(), cmd.Env...)
	execution.Stdin = cmd.Stdin

	var stdout, stderr bytes.Buffer
	if cmd.Stdout != nil {
		execution.Stdout = cmd.Stdout
	} else {
		execution.Stdout = &stdout
	}
	if cmd.Stderr != nil {
		execution.Stderr = cmd.Stderr
	} else {
		execution.Stderr = &stderr
	}

	err := execution.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		result.ExitCode = exit.ExitCode()
		return result, nil
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func (Local) ReadFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (Local) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	// WriteFile permissions do not apply to a pre-existing file; chmod
	// keeps rewritten configs at their declared mode.
	return os.Chmod(path, perm)
}

func (Local) ReplaceFile(_ context.Context, path, backup string, data []byte, perm fs.FileMode) error {
	if backup != "" {
		if current, err := os.ReadFile(path); err == nil {
			if err := replaceLocalFile(backup, current, perm); err != nil {
				return err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return replaceLocalFile(path, data, perm)
}

func replaceLocalFile(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(perm); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	if directory, err := os.Open(dir); err == nil {
		defer directory.Close()
		return directory.Sync()
	}
	return nil
}

func (Local) MkdirAll(_ context.Context, path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (Local) Remove(_ context.Context, path string) error {
	return os.RemoveAll(path)
}

func (Local) Stat(_ context.Context, path string) (Info, error) {
	stat, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Info{}, nil
	}
	if err != nil {
		return Info{}, err
	}
	return Info{Exists: true, Mode: stat.Mode(), Size: stat.Size()}, nil
}

func (Local) ProbeHTTP(ctx context.Context, request HTTPRequest) (HTTPResponse, error) {
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if request.Insecure {
		tlsConfig.InsecureSkipVerify = true // bootstrap fetch; the caller validates the CA hash.
	} else if len(request.CACerts) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(request.CACerts) {
			return HTTPResponse{}, errors.New("HTTP probe CA bundle contains no certificates")
		}
		tlsConfig.RootCAs = pool
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig:   tlsConfig,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// Bootstrap probes authenticate one exact origin. Following a
			// redirect could validate or disclose credentials to a target
			// the operator did not supply.
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, request.URL, nil)
	if err != nil {
		return HTTPResponse{}, err
	}
	switch {
	case request.Bearer != "":
		req.Header.Set("Authorization", "Bearer "+request.Bearer)
	case request.Username != "":
		req.SetBasicAuth(request.Username, request.Password)
	}
	response, err := client.Do(req)
	if err != nil {
		return HTTPResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return HTTPResponse{}, err
	}
	return HTTPResponse{StatusCode: response.StatusCode, Body: body}, nil
}

// cleanPath normalizes fake filesystem keys so tests and engine agree.
func cleanPath(path string) string {
	return filepath.Clean(path)
}
