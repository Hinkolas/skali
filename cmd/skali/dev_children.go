package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/utils"
)

// childGrace is how long a terminating dev child gets between SIGTERM and
// SIGKILL.
const childGrace = 10 * time.Second

// devChildren are the dev-block applications running on the host for this
// session. They never outlive the CLI: detach and the session epilogue
// terminate them before anything else happens.
type devChildren struct {
	children []*devChild
}

type devChild struct {
	key  string
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// startDevChildren resolves each application's environment through the
// local platform (sudo-gated like credential reveal; reauthLocal covers the
// window non-interactively) and starts its dev command with stdout and
// stderr multiplexed. Called after the deploy settled so the just-staged
// values and revision are active. A crashed child is reported through the
// mux and the session keeps running; nothing restarts automatically.
func startDevChildren(ctx context.Context, mux *logMux, out io.Writer, api *client.Client,
	environmentID, root string, apps map[string]manifest.Dev,
	ports map[string]map[string]int) (*devChildren, error) {
	style := clirender.StyleFor(out)
	group := &devChildren{}
	for _, key := range utils.SortedKeys(apps) {
		dev := apps[key]
		resolved, err := api.ApplicationEnvironment(ctx, environmentID, key, localdev.LoopbackPortBase())
		if isReauthRequired(err) {
			if err = reauthLocal(ctx, api); err == nil {
				resolved, err = api.ApplicationEnvironment(ctx, environmentID, key, localdev.LoopbackPortBase())
			}
		}
		if err != nil {
			group.Terminate(childGrace)
			return nil, fmt.Errorf("resolve environment for %s: %w", key, err)
		}
		for _, warning := range resolved.Warnings {
			fmt.Fprintf(out, "  %s\n", style.Yellow("warning: "+key+": "+warning))
		}
		environ, lookup := devChildEnviron(resolved.Values, ports[key])
		argv, err := expandDevCommand(key, dev.Command, lookup, ports[key])
		if err != nil {
			group.Terminate(childGrace)
			return nil, err
		}
		child, err := startDevChild(ctx, mux, key, argv, root, environ)
		if err != nil {
			group.Terminate(childGrace)
			return nil, err
		}
		group.children = append(group.children, child)
		fmt.Fprintf(out, "  %s      %s\n", style.Dim("local"), key+": "+strings.Join(argv, " "))
		go watchDevChild(ctx, mux, child)
		go probeDevPorts(ctx, mux, child, ports[key])
	}
	return group, nil
}

// devChildEnviron layers the child environment: the session's own
// environment, the resolved application values, then the injected port
// variables, which win (os/exec keeps the last duplicate). It returns
// both the exec env slice and the flat lookup map ${VAR} expansion reads.
func devChildEnviron(resolved map[string]string, ports map[string]int) ([]string, map[string]string) {
	lookup := map[string]string{}
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok {
			lookup[name] = value
		}
	}
	environ := os.Environ()
	for _, name := range utils.SortedKeys(resolved) {
		environ = append(environ, name+"="+resolved[name])
		lookup[name] = resolved[name]
	}
	for _, name := range utils.SortedKeys(ports) {
		value := strconv.Itoa(ports[name])
		environ = append(environ, "SKALI_PORT_"+portEnvName(name)+"="+value)
		lookup["SKALI_PORT_"+portEnvName(name)] = value
		if len(ports) == 1 {
			environ = append(environ, "PORT="+value)
			lookup["PORT"] = value
		}
	}
	return environ, lookup
}

// portEnvName maps a service port name to its env var suffix. Port keys
// contain only lowercase letters, digits, and hyphens, so the mapping
// cannot collide.
func portEnvName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// expandDevCommand substitutes ${VAR} in every dev.command element from
// the child's environment, so the manifest can hand the injected port to
// tools that only take it as a flag.
func expandDevCommand(key string, command []string, lookup map[string]string,
	ports map[string]int) ([]string, error) {
	argv := make([]string, 0, len(command))
	for _, argument := range command {
		expanded, err := compiler.ExpandVariables(argument, func(name string) (string, bool) {
			value, ok := lookup[name]
			return value, ok
		})
		if err != nil {
			injected := make([]string, 0, len(ports))
			for _, name := range utils.SortedKeys(ports) {
				injected = append(injected, "SKALI_PORT_"+portEnvName(name))
			}
			return nil, fmt.Errorf("dev command for %s: %w in %q; available injected ports: %s (use ${NAME:-default} for optional values)",
				key, err, argument, strings.Join(injected, ", "))
		}
		argv = append(argv, expanded)
	}
	return argv, nil
}

func startDevChild(ctx context.Context, mux *logMux, key string, argv []string,
	root string, environ []string) (*devChild, error) {
	// CommandContext alone would SIGKILL on context end; configureChildProcess
	// replaces that with a process-group SIGTERM and a grace period, so the
	// dev server (and its grandchildren, e.g. a shell-wrapped node) can shut
	// down cleanly.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = environ
	configureChildProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("dev command for %s: %w", key, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("dev command for %s: %w", key, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start dev command for %s: %w", key, err)
	}
	child := &devChild{key: key, cmd: cmd, done: make(chan struct{})}
	writer := mux.Writer(key + " | ")
	var wg sync.WaitGroup
	wg.Add(2)
	pipe := func(source io.Reader) {
		defer wg.Done()
		_, _ = io.Copy(writer, source)
	}
	go pipe(stdout)
	go pipe(stderr)
	go func() {
		wg.Wait()
		writer.Flush()
		child.err = cmd.Wait()
		close(child.done)
	}()
	return child, nil
}

// probeDevPorts warns when a just-started dev child is not reachable on
// its allocated ports. The intercept routes cluster traffic to the host
// port over the container network, so only a non-loopback listener
// counts as reachable (loopback success alone is the classic vite
// default-bind trap). Warning only; the child is never killed, and a
// child that exits ends the probe silently since watchDevChild already
// reports it.
func probeDevPorts(ctx context.Context, mux *logMux, child *devChild, ports map[string]int) {
	if len(ports) == 0 {
		return
	}
	hostIP := nonLoopbackIP()
	deadline := time.After(10 * time.Second)
	pending := map[string]bool{} // port name -> loopback answered
	for _, name := range utils.SortedKeys(ports) {
		pending[name] = false
	}
	for len(pending) > 0 {
		for _, name := range utils.SortedKeys(pending) {
			port := strconv.Itoa(ports[name])
			if hostIP != "" {
				if conn, err := net.DialTimeout("tcp", net.JoinHostPort(hostIP, port), 500*time.Millisecond); err == nil {
					_ = conn.Close()
					delete(pending, name)
					continue
				}
			}
			if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 500*time.Millisecond); err == nil {
				_ = conn.Close()
				if hostIP == "" {
					delete(pending, name)
					continue
				}
				pending[name] = true
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-child.done:
			return
		case <-deadline:
			for _, name := range utils.SortedKeys(pending) {
				if pending[name] {
					mux.writeLine(child.key+" | ", fmt.Sprintf(
						"warning: port %s (%d) answers only on loopback; in-cluster routes cannot reach it, bind the dev server to 0.0.0.0 (for vite: --host)",
						name, ports[name]))
					continue
				}
				mux.writeLine(child.key+" | ", fmt.Sprintf(
					"warning: nothing is listening on port %s (%d); in-cluster routes to %s will fail until the dev server listens there",
					name, ports[name], child.key))
			}
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// nonLoopbackIP returns one of the host's non-loopback IPv4 addresses,
// or empty when the host has none (offline machine); loopback
// reachability is the best remaining approximation there.
func nonLoopbackIP() string {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, address := range addresses {
		network, ok := address.(*net.IPNet)
		if !ok || network.IP.IsLoopback() {
			continue
		}
		if ip := network.IP.To4(); ip != nil {
			return ip.String()
		}
	}
	return ""
}

// watchDevChild reports a child that exited while the session lives; the
// rest of the session (cluster logs, other children) keeps running, exactly
// like a crashing pod would not end the follow.
func watchDevChild(ctx context.Context, mux *logMux, child *devChild) {
	select {
	case <-ctx.Done():
	case <-child.done:
		if ctx.Err() != nil {
			return
		}
		detail := "exited"
		if child.err != nil {
			detail = "exited: " + child.err.Error()
		}
		mux.writeLine(child.key+" | ", "dev command "+detail+
			" (the session keeps running; fix it and restart skali dev)")
	}
}

// Terminate stops every child: SIGTERM to each process group, one shared
// grace period, then SIGKILL for stragglers. Safe on nil and safe to call
// more than once.
func (c *devChildren) Terminate(grace time.Duration) {
	if c == nil {
		return
	}
	for _, child := range c.children {
		terminateChild(child.cmd)
	}
	deadline := time.After(grace)
	for _, child := range c.children {
		select {
		case <-child.done:
		case <-deadline:
			killChild(child.cmd)
			<-child.done
		}
	}
}
