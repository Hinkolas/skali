package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/devremote"
	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/Hinkolas/skali/internal/truststore"
)

// Remote development: a skali dev platform on one machine, the browser on
// another (see internal/devremote and docs/remote-development.md). skali
// dev proxy runs on the machine with the browser, skali dev expose on the
// machine with the platform.

func newDevProxyCommand() *cobra.Command {
	var caFlag string
	var detach bool
	command := &cobra.Command{
		Use:   "proxy [host]",
		Short: "Open a skali dev platform on another machine at its *.localhost origins",
		Long: "Makes this machine the entrypoint of a skali dev platform that runs on\n" +
			"another machine, without a cluster or Docker here. The proxy listens on\n" +
			"this machine's ports 443 and 80 and relays raw TCP to the same ports on\n" +
			"host, so https://<domain>.localhost in a browser here is the same origin\n" +
			"it is on the other machine: TLS ends at the remote edge and nothing is\n" +
			"rewritten.\n\n" +
			"The remote edge only listens on that machine's loopback, so something there\n" +
			"has to pass outside traffic to it: skali dev expose <address>, or tailscale\n" +
			"serve. How this machine reaches host (a LAN, a VPN, a tailnet) is up to you.\n\n" +
			"Browsers here must trust the remote platform's development CA. The proxy\n" +
			"fetches it from host and accepts it only when it matches --ca, the\n" +
			"fingerprint skali dev status prints on the other machine, then installs it\n" +
			"like skali dev trust. Host and fingerprint are remembered: a bare skali dev\n" +
			"proxy reuses them. -d runs the proxy in the background; skali dev proxy\n" +
			"stop ends it.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeProxyHost,
		RunE: func(command *cobra.Command, args []string) error {
			return runDevProxy(command, args, caFlag, detach)
		},
	}
	command.Flags().StringVar(&caFlag, "ca", "",
		"fingerprint of the remote platform's development CA, as skali dev status prints it there (sha256:…); remembered after the first run")
	command.Flags().BoolVarP(&detach, "detach", "d", false, "run in the background; skali dev proxy stop ends it")
	command.AddCommand(newDevSessionStopCommand(devremote.ProxySession, "Stop the proxy running in the background"))
	return command
}

func newDevExposeCommand() *cobra.Command {
	var detach bool
	command := &cobra.Command{
		Use:   "expose [address]",
		Short: "Let skali dev proxy on another machine reach this machine's local platform",
		Long: "Opens this machine's local platform to skali dev proxy on another machine.\n" +
			"The platform's edge only listens on loopback; expose listens on ports 443\n" +
			"and 80 of one address of this machine, such as its LAN or VPN address, and\n" +
			"relays raw TCP to the edge. It prints the skali dev proxy command to run on\n" +
			"the other machine, including the CA fingerprint.\n\n" +
			"Anyone who reaches that address reaches the development edge: the Studio\n" +
			"and every route of every project on the local platform. Pick an address\n" +
			"only your own machines reach; the wildcard address is refused. The address\n" +
			"is remembered: a bare skali dev expose reuses it. -d runs it in the\n" +
			"background; skali dev expose stop ends it. Tailscale users can run\n" +
			"tailscale serve instead.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeExposeAddress,
		RunE: func(command *cobra.Command, args []string) error {
			return runDevExpose(command, args, detach)
		},
	}
	command.Flags().BoolVarP(&detach, "detach", "d", false, "run in the background; skali dev expose stop ends it")
	command.AddCommand(newDevSessionStopCommand(devremote.ExposeSession, "Stop exposing the local platform"))
	return command
}

func newDevSessionStopCommand(open func() (devremote.Session, error), short string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			session, err := open()
			if err != nil {
				return err
			}
			return stopDevSession(command.OutOrStdout(), session)
		},
	}
}

// --- trust ------------------------------------------------------------

// runRemoteTrust is skali dev trust <host>: the remote platform's CA,
// verified against its fingerprint, into this machine's trust stores.
func runRemoteTrust(ctx context.Context, out io.Writer, hostArg, caArg string, checkOnly bool) error {
	host, err := devremote.ParseHost(hostArg)
	if err != nil {
		return err
	}
	if caArg == "" {
		return errors.New("--ca is required with a host: run skali dev status on the other machine and copy the fingerprint it prints")
	}
	fingerprint, err := devremote.ParseFingerprint(caArg)
	if err != nil {
		return err
	}
	ca, path, err := pinRemoteCA(ctx, host, fingerprint)
	if err != nil {
		return err
	}
	certificate := truststore.Certificate{Path: path, PEM: ca.PEM, Name: ca.CommonName()}
	var status truststore.Status
	if checkOnly {
		status, err = truststore.Check(ctx, certificate)
	} else {
		status, err = truststore.Install(ctx, certificate)
	}
	if err != nil {
		return err
	}
	printTrustStatus(out, clirender.StyleFor(out), status, path, ca.CommonName(), ca.Fingerprint)
	if !status.Trusted() {
		if checkOnly {
			return fmt.Errorf("the development CA of the platform at %s is not trusted; run skali dev trust %s --ca %s", host, host, fingerprint)
		}
		return errors.New("the development CA could not be trusted everywhere; import " + path + " by hand where it says so")
	}
	return nil
}

// pinRemoteCA fetches the CA of the platform reachable at host, verified
// against fingerprint, and keeps it where the trust stores import it from.
func pinRemoteCA(ctx context.Context, host, fingerprint string) (*devremote.RemoteCA, string, error) {
	ca, err := devremote.FetchCA(ctx, host, fingerprint)
	if err != nil {
		return nil, "", err
	}
	path, err := ca.Pin()
	if err != nil {
		return nil, "", err
	}
	return ca, path, nil
}

// trustRemoteCA makes sure browsers here trust the remote CA: nothing to do
// when they already do, an install when a person is present (the OS asks
// for a password), a warning otherwise. Never fatal: the proxy relays
// either way, and the warning says how to finish.
func trustRemoteCA(ctx context.Context, out io.Writer, ca *devremote.RemoteCA, path, host string) bool {
	certificate := truststore.Certificate{Path: path, PEM: ca.PEM, Name: ca.CommonName()}
	status, err := truststore.Check(ctx, certificate)
	if err == nil && status.Trusted() {
		return true
	}
	style := clirender.StyleFor(out)
	if err == nil && cliprompt.Interactive() {
		fmt.Fprintln(out, "Trusting the remote platform's development CA so browsers here accept https://*.localhost."+
			trustPromptHint())
		task := clirender.NewTasks(out).Start("Trust development CA")
		status, err = truststore.Install(ctx, certificate)
		if err == nil && status.Trusted() {
			task.Done(ca.CommonName())
			return true
		}
		task.Fail()
		if err != nil {
			fmt.Fprintf(out, "  %s\n", style.Yellow("warning: "+err.Error()))
		} else {
			printTrustStatus(out, style, status, path, ca.CommonName(), ca.Fingerprint)
		}
	}
	fmt.Fprintf(out, "  %s\n", style.Yellow(fmt.Sprintf(
		"warning: browsers on this machine do not trust the remote platform's development CA yet; "+
			"run skali dev trust %s --ca %s, or import %s yourself", host, ca.Fingerprint, path)))
	return false
}

// --- proxy ------------------------------------------------------------

func runDevProxy(command *cobra.Command, args []string, caFlag string, detach bool) error {
	ctx := command.Context()
	out := command.OutOrStdout()
	session, err := devremote.ProxySession()
	if err != nil {
		return err
	}
	host, fingerprint, err := proxyTarget(session, args, caFlag)
	if err != nil {
		return err
	}
	if forwardsToItself(host) {
		return fmt.Errorf("%s is this machine: the proxy would forward ports %d and %d to themselves. "+
			"When an SSH forward already brings the remote edge to this machine, only the CA is missing: "+
			"skali dev trust %s --ca %s", host, localdev.HTTPSPort(), localdev.HTTPPort(), host, fingerprint)
	}
	if err := refuseRunningSession(session); err != nil {
		return err
	}
	routes := devremote.ProxyRoutes(host)
	if detach {
		return startDetachedProxy(ctx, out, session, host, fingerprint, routes)
	}
	release, err := session.Acquire()
	if err != nil {
		return sessionAcquireError(session, err)
	}
	defer release()
	listeners, err := devremote.Listen(routes)
	if err != nil {
		return explainProxyBind(err)
	}
	ca, path, err := pinRemoteCA(ctx, host, fingerprint)
	if err != nil {
		closeListeners(listeners)
		return err
	}
	if err := session.Save(devremote.Record{Host: host, Fingerprint: fingerprint, PID: os.Getpid(), Started: time.Now().UTC()}); err != nil {
		closeListeners(listeners)
		return err
	}
	trusted := trustRemoteCA(ctx, out, ca, path, host)
	printProxyReady(out, routes, ca, trusted, 0, "")
	fmt.Fprintln(out, clirender.StyleFor(out).Dim("Ctrl-C stops the proxy."))
	signalDetachedReady()
	serveSession(ctx, out, routes, listeners)
	fmt.Fprintln(out, "proxy stopped")
	return nil
}

// forwardsToItself reports a target on this machine's loopback at the
// ports the proxy listens on, which would relay every connection back into
// the proxy until it runs out of descriptors.
func forwardsToItself(host string) bool {
	loopback := host == "localhost" || strings.HasSuffix(host, ".localhost")
	if addr, err := netip.ParseAddr(host); err == nil {
		loopback = addr.IsLoopback()
	}
	return loopback && (devremote.RemoteHTTPSPort() == localdev.HTTPSPort() || devremote.RemoteHTTPPort() == localdev.HTTPPort())
}

// proxyTarget resolves host and fingerprint from the arguments, falling
// back to the ones the last run remembered.
func proxyTarget(session devremote.Session, args []string, caFlag string) (host, fingerprint string, err error) {
	recorded, err := session.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if recorded == nil {
		recorded = &devremote.Record{}
	}
	switch {
	case len(args) == 1:
		if host, err = devremote.ParseHost(args[0]); err != nil {
			return "", "", err
		}
	case recorded.Host != "":
		host = recorded.Host
	default:
		return "", "", errors.New("pass the host the other machine's platform is reached at: " +
			"run skali dev status there and copy the skali dev proxy command it prints")
	}
	switch {
	case caFlag != "":
		if fingerprint, err = devremote.ParseFingerprint(caFlag); err != nil {
			return "", "", err
		}
	case recorded.Fingerprint != "":
		fingerprint = recorded.Fingerprint
	default:
		return "", "", errors.New("--ca is required the first time: run skali dev status on the other machine " +
			"and copy the skali dev proxy command it prints, fingerprint included")
	}
	return host, fingerprint, nil
}

// startDetachedProxy runs the checks a person can act on in the
// foreground (the ports, the remote CA, the trust prompt) and then hands
// over to a background process, which repeats the first two and serves.
func startDetachedProxy(ctx context.Context, out io.Writer, session devremote.Session, host, fingerprint string, routes []devremote.Route) error {
	listeners, err := devremote.Listen(routes)
	if err != nil {
		return explainProxyBind(err)
	}
	closeListeners(listeners)
	ca, path, err := pinRemoteCA(ctx, host, fingerprint)
	if err != nil {
		return err
	}
	if err := session.Save(devremote.Record{Host: host, Fingerprint: fingerprint}); err != nil {
		return err
	}
	trusted := trustRemoteCA(ctx, out, ca, path, host)
	pid, err := startDetached(ctx, session.LogPath(), []string{"dev", "proxy", host, "--ca", fingerprint})
	if err != nil {
		return err
	}
	printProxyReady(out, routes, ca, trusted, pid, session.LogPath())
	return nil
}

func printProxyReady(out io.Writer, routes []devremote.Route, ca *devremote.RemoteCA, trusted bool, pid int, logPath string) {
	style := clirender.StyleFor(out)
	targets := make([]string, 0, len(routes))
	for _, route := range routes {
		targets = append(targets, route.Target)
	}
	headline := "Forwarding *.localhost to " + strings.Join(targets, " and ")
	if pid > 0 {
		headline = fmt.Sprintf("Proxy running in the background (pid %d), forwarding *.localhost to %s",
			pid, strings.Join(targets, " and "))
	}
	fmt.Fprintf(out, "%s%s\n", style.Check(), headline)
	trust := style.Green("trusted")
	if !trusted {
		trust = style.Yellow("not trusted by browsers here")
	}
	fmt.Fprintf(out, "  %s %s  %s\n", style.Dim("CA     "), ca.CommonName(), trust)
	fmt.Fprintf(out, "  %s %s\n", style.Dim("Studio "),
		style.Link("https://"+bundle.LocalPlatformHost+localdev.HostPortSuffix(localdev.HTTPSPort(), 443)))
	if logPath != "" {
		fmt.Fprintf(out, "  %s %s\n", style.Dim("log    "), logPath)
		fmt.Fprintln(out, style.Dim("skali dev proxy stop ends it."))
	}
}

// explainProxyBind turns a failed listen on the edge ports into what to do
// about it.
func explainProxyBind(err error) error {
	bind, ok := errors.AsType[*devremote.BindError](err)
	if !ok {
		return err
	}
	_, port, _ := net.SplitHostPort(bind.Route.Listen)
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		if _, stateErr := localdev.LoadState(); stateErr == nil {
			return fmt.Errorf("port %s is in use, most likely by this machine's own skali dev platform, whose edge listens there; "+
				"stop it with skali dev stop and run skali dev proxy again (lsof -nP -iTCP:%s -sTCP:LISTEN names the process)", port, port)
		}
		return fmt.Errorf("port %s is in use on this machine, but skali dev proxy has to listen there for https://*.localhost to reach it; "+
			"free it (lsof -nP -iTCP:%s -sTCP:LISTEN names the process) and run skali dev proxy again", port, port)
	case errors.Is(err, syscall.EACCES):
		return privilegedPortError(port, "skali dev proxy")
	}
	return err
}

// --- expose -----------------------------------------------------------

func runDevExpose(command *cobra.Command, args []string, detach bool) error {
	ctx := command.Context()
	out := command.OutOrStdout()
	session, err := devremote.ExposeSession()
	if err != nil {
		return err
	}
	addr, err := exposeAddress(session, args)
	if err != nil {
		return err
	}
	ca, err := localdev.LoadCA()
	if errors.Is(err, localdev.ErrNotInstalled) {
		return errors.New("there is no local platform on this machine to expose; run skali dev first")
	}
	if err != nil {
		return err
	}
	if err := refuseRunningSession(session); err != nil {
		return err
	}
	routes := devremote.ExposeRoutes(addr)
	if detach {
		listeners, err := devremote.Listen(routes)
		if err != nil {
			return explainExposeBind(err, addr)
		}
		closeListeners(listeners)
		if err := session.Save(devremote.Record{Host: addr.String()}); err != nil {
			return err
		}
		pid, err := startDetached(ctx, session.LogPath(), []string{"dev", "expose", addr.String()})
		if err != nil {
			return err
		}
		printExposeReady(out, addr, routes, ca, pid, session.LogPath())
		return nil
	}
	release, err := session.Acquire()
	if err != nil {
		return sessionAcquireError(session, err)
	}
	defer release()
	listeners, err := devremote.Listen(routes)
	if err != nil {
		return explainExposeBind(err, addr)
	}
	if err := session.Save(devremote.Record{Host: addr.String(), PID: os.Getpid(), Started: time.Now().UTC()}); err != nil {
		closeListeners(listeners)
		return err
	}
	printExposeReady(out, addr, routes, ca, 0, "")
	fmt.Fprintln(out, clirender.StyleFor(out).Dim("Ctrl-C stops exposing."))
	signalDetachedReady()
	serveSession(ctx, out, routes, listeners)
	fmt.Fprintln(out, "stopped exposing the local platform")
	return nil
}

func exposeAddress(session devremote.Session, args []string) (netip.Addr, error) {
	if len(args) == 1 {
		return devremote.ParseExposeAddress(args[0])
	}
	recorded, err := session.Load()
	if errors.Is(err, os.ErrNotExist) || (err == nil && recorded.Host == "") {
		return netip.Addr{}, errors.New("pass the address of this machine the other machine connects to, " +
			"for example its LAN or VPN address: skali dev expose <address>")
	}
	if err != nil {
		return netip.Addr{}, err
	}
	return devremote.ParseExposeAddress(recorded.Host)
}

func printExposeReady(out io.Writer, addr netip.Addr, routes []devremote.Route, ca *localdev.CA, pid int, logPath string) {
	style := clirender.StyleFor(out)
	listens := make([]string, 0, len(routes))
	for _, route := range routes {
		listens = append(listens, route.Listen)
	}
	headline := "Exposing the local platform on " + strings.Join(listens, " and ")
	if pid > 0 {
		headline = fmt.Sprintf("Exposing the local platform in the background (pid %d) on %s",
			pid, strings.Join(listens, " and "))
	}
	fmt.Fprintf(out, "%s%s\n", style.Check(), headline)
	fmt.Fprintf(out, "  %s %s\n", style.Dim("on the other machine:"),
		proxyCommandLine(addr.String(), devremote.FingerprintFromHex(ca.Fingerprint)))
	if !edgeAnswers(routes[0].Target) {
		fmt.Fprintf(out, "  %s\n", style.Yellow("warning: the local platform does not answer on "+routes[0].Target+
			" yet; start it with skali dev start, connections fail until then"))
	}
	fmt.Fprintf(out, "  %s\n", style.Yellow("Anyone who reaches "+addr.String()+" reaches the development edge and the Studio."))
	if logPath != "" {
		fmt.Fprintf(out, "  %s %s\n", style.Dim("log"), logPath)
		fmt.Fprintln(out, style.Dim("skali dev expose stop ends it."))
	}
}

// edgeAnswers reports whether something accepts connections on the local
// edge; a TCP connect, so it needs no Docker query.
func edgeAnswers(address string) bool {
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func explainExposeBind(err error, addr netip.Addr) error {
	bind, ok := errors.AsType[*devremote.BindError](err)
	if !ok {
		return err
	}
	_, port, _ := net.SplitHostPort(bind.Route.Listen)
	switch {
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return fmt.Errorf("%s is not an address of this machine; pass one of its own addresses "+
			"(ifconfig or ip addr lists them, tailscale ip -4 names the tailnet one)", addr)
	case errors.Is(err, syscall.EADDRINUSE):
		return fmt.Errorf("port %s on %s is in use (lsof -nP -iTCP:%s -sTCP:LISTEN names the process); "+
			"if tailscale serve already passes it to the edge, skali dev expose is not needed", port, addr, port)
	case errors.Is(err, syscall.EACCES):
		return privilegedPortError(port, "skali dev expose")
	}
	return err
}

// --- shared -------------------------------------------------------------

// proxyCommandLine is the command the other machine runs.
func proxyCommandLine(host, fingerprint string) string {
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "skali dev proxy " + host + " --ca " + fingerprint
}

func privilegedPortError(port, command string) error {
	return fmt.Errorf("listening on port %s needs privileges on this system; allow unprivileged web ports with "+
		"sudo sysctl -w net.ipv4.ip_unprivileged_port_start=80 (a file in /etc/sysctl.d keeps it) and run %s again", port, command)
}

func refuseRunningSession(session devremote.Session) error {
	record, running, err := session.Running()
	if err != nil || !running {
		return err
	}
	return fmt.Errorf("skali dev %s is already running%s; skali dev %s stop ends it",
		session.Name(), describeSession(record), session.Name())
}

func sessionAcquireError(session devremote.Session, err error) error {
	if errors.Is(err, devremote.ErrRunning) {
		return fmt.Errorf("skali dev %s is already running; skali dev %s stop ends it", session.Name(), session.Name())
	}
	return err
}

func describeSession(record *devremote.Record) string {
	switch {
	case record == nil || (record.PID == 0 && record.Host == ""):
		return ""
	case record.PID == 0:
		return " (" + record.Host + ")"
	}
	return fmt.Sprintf(" (%s, pid %d)", record.Host, record.PID)
}

func closeListeners(listeners []net.Listener) {
	for _, listener := range listeners {
		listener.Close()
	}
}

// serveSession relays until Ctrl-C or a stop, reporting reachability
// changes with a time of day: in the background they land in the log.
func serveSession(ctx context.Context, out io.Writer, routes []devremote.Route, listeners []net.Listener) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	logf := func(format string, args ...any) {
		fmt.Fprintf(out, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}
	devremote.Serve(ctx, routes, listeners, logf)
}

// stopDevSession ends a session running in the background and waits until
// it released its ports.
func stopDevSession(out io.Writer, session devremote.Session) error {
	record, running, err := session.Running()
	if err != nil {
		return err
	}
	if !running {
		fmt.Fprintf(out, "skali dev %s is not running\n", session.Name())
		return nil
	}
	if record == nil || record.PID <= 0 {
		return fmt.Errorf("skali dev %s is running but recorded no process id; stop it where it runs", session.Name())
	}
	process, err := os.FindProcess(record.PID)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop skali dev %s (pid %d): %w", session.Name(), record.PID, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, running, err := session.Running(); err == nil && !running {
			fmt.Fprintf(out, "%sstopped skali dev %s (pid %d)\n", clirender.StyleFor(out).Check(), session.Name(), record.PID)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("skali dev %s (pid %d) did not stop within 10 seconds", session.Name(), record.PID)
}

// refuseWhileProxying keeps the local platform from starting while the
// proxy holds its edge ports; k3d would otherwise fail on the port
// mappings with an error that does not name the proxy.
func refuseWhileProxying() error {
	session, err := devremote.ProxySession()
	if err != nil {
		return err
	}
	record, running, err := session.Running()
	if err != nil || !running {
		return err
	}
	target := "another machine"
	if record != nil && record.Host != "" {
		target = record.Host
	}
	return fmt.Errorf("skali dev proxy forwards this machine's *.localhost to %s, so the local platform cannot use ports %d and %d; "+
		"stop it with skali dev proxy stop first", target, localdev.HTTPSPort(), localdev.HTTPPort())
}

// printRemoteDevSessions reports the proxy and expose processes running on
// this machine; nothing when neither runs. It needs no Docker.
func printRemoteDevSessions(out io.Writer, style *clirender.Style) {
	if session, err := devremote.ProxySession(); err == nil {
		if record, running, err := session.Running(); err == nil && running && record != nil {
			fmt.Fprintf(out, "proxy      %s (forwarding *.localhost to %s, pid %d, CA %s)\n",
				stateColor(style, "running"), record.Host, record.PID, shortFingerprint(record.Fingerprint))
		}
	}
	if session, err := devremote.ExposeSession(); err == nil {
		if record, running, err := session.Running(); err == nil && running && record != nil {
			fmt.Fprintf(out, "expose     %s (local platform on %s, pid %d)\n",
				stateColor(style, "running"), record.Host, record.PID)
		}
	}
}

// printRemoteCommand prints the skali dev proxy command for another
// machine, with the address expose listens on when it runs.
func printRemoteCommand(out io.Writer) {
	ca, err := localdev.LoadCA()
	if err != nil {
		return
	}
	host := "<host>"
	if session, err := devremote.ExposeSession(); err == nil {
		if record, running, err := session.Running(); err == nil && running && record != nil && record.Host != "" {
			host = record.Host
		}
	}
	fmt.Fprintf(out, "remote     %s\n", proxyCommandLine(host, devremote.FingerprintFromHex(ca.Fingerprint)))
}

func shortFingerprint(fingerprint string) string {
	const shown = len("sha256:") + 12
	if len(fingerprint) <= shown {
		return fingerprint
	}
	return fingerprint[:shown] + "…"
}

// completeProxyHost offers the host the proxy last used.
func completeProxyHost(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var hosts []cobra.Completion
	if session, err := devremote.ProxySession(); err == nil {
		if record, err := session.Load(); err == nil && record.Host != "" {
			hosts = append(hosts, cobra.Completion(record.Host))
		}
	}
	return hosts, cobra.ShellCompDirectiveNoFileComp
}

// completeExposeAddress offers the addresses other machines may reach this
// one on: every interface address except loopback and link-local ones.
func completeExposeAddress(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var completions []cobra.Completion
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil {
			continue
		}
		addr := prefix.Addr()
		if addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() || addr.IsMulticast() {
			continue
		}
		completions = append(completions, cobra.Completion(addr.String()))
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}
