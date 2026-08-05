package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/registrytoken"
	"github.com/Hinkolas/skali/internal/utils"
	"github.com/Hinkolas/skali/internal/version"
)

// Default installer-owned volume sizes for production installations.
const (
	DefaultDatabaseStorage = "10Gi"
	DefaultRegistryStorage = "20Gi"
)

// InitOptions parameterize one cluster initialization.
type InitOptions struct {
	Endpoints Endpoints
	TLS       TLSConfig
	// SkalidImage is the control-plane image reference; SkalidImageID
	// optionally pins the content identity behind a mutable tag.
	SkalidImage   string
	SkalidImageID string
	// WebImage is the web console image reference serving the platform
	// domain root; WebImageID optionally pins its content identity.
	WebImage   string
	WebImageID string
	// RegistryNode pins the local registry volume to the coordinator's
	// durable placement choice. Empty retains legacy capability-only
	// placement.
	RegistryNode string
	// Layout optionally asserts the expected membership; init refuses when
	// the joined nodes do not match it.
	Layout *layout.Layout
	// Admin returns the first operator account credentials. It is called
	// only after skalid is ready, so interactive runs prompt at the moment
	// the transcript shows; the credentials are never persisted host-side.
	Admin func(ctx context.Context) (email, password string, err error)
	// SkipAdmin skips the admin bootstrap step; upgrade reuses the
	// existing accounts and never collects credentials.
	SkipAdmin bool
	// Progress narrates stages; Out receives the layout and topology
	// tables.
	Progress Progress
	Out      io.Writer
}

// InitResult reports where the initialized installation answers.
type InitResult struct {
	// ConsoleURL is the web console at the platform domain root; APIURL is
	// the daemon behind the /api path on the same domain.
	ConsoleURL  string
	APIURL      string
	RegistryURL string
	LogPath     string
}

// Init initializes Skali on a joined cluster: it rebuilds the layout from
// node labels, derives the topology, converges the production bundle,
// creates the admin account, and updates both record copies. Init runs
// before skalid or its database serve anything, so every step is logged
// locally under LogDir, never in the product run journal. Repeat init is a
// converge: server-side applies no-op, the auth secret is reused, and the
// bootstrap job is idempotent.
func Init(ctx context.Context, runner host.Runner, record *Record, opts InitOptions) (*InitResult, error) {
	if record == nil {
		return nil, errors.New("init requires an installation record; run install first")
	}
	if record.Node.Role != layout.RoleServer {
		return nil, errors.New("init must run on a server node")
	}
	if err := ValidateInitOptions(opts); err != nil {
		return nil, err
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	log, err := newInitLog(ctx, runner)
	if err != nil {
		return nil, err
	}
	progress := &loggingProgress{inner: opts.Progress, log: log}
	if opts.Progress == nil {
		progress.inner = silentProgress{}
	}
	fail := func(err error) (*InitResult, error) {
		log.line("error: " + err.Error())
		log.flush(ctx)
		return nil, err
	}

	client, err := KubeClient(ctx, runner)
	if err != nil {
		return fail(err)
	}

	nodeList, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fail(fmt.Errorf("list nodes: %w", err))
	}
	if err := assertClusterMembership(nodeList.Items, record.Cluster); err != nil {
		return fail(err)
	}
	// The init-owner rule: the canonical record embeds this node's
	// identity and is a bundle-hash input, so a converge from a second
	// server would churn the published record and hash. The bundle stays
	// maintained by the node that first initialized it; other servers
	// upgrade k3s only.
	if !record.Reconciled() && record.Versions.Bundle == "" {
		if published, err := InClusterRecord(ctx, client); err == nil && published != nil &&
			published.Node.Name != "" && published.Node.Name != record.Node.Name {
			return fail(fmt.Errorf("this cluster was initialized from %s; run init and upgrade there",
				published.Node.Name))
		}
	}
	live := LayoutFromNodes(nodeList.Items, record.Cluster)
	printLayout(out, live)
	log.line("cluster layout: " + compactLayout(live))
	if opts.Layout != nil {
		if err := assertLayout(*opts.Layout, live); err != nil {
			return fail(err)
		}
	}

	topology := live.Topology()
	registryNode := opts.RegistryNode
	if registryNode == "" {
		registryNode = firstCapableNode(live, layout.CapabilityRegistry)
	}
	if node, ok := live.Nodes[registryNode]; !ok ||
		!slices.Contains(node.Capabilities, layout.CapabilityRegistry) {
		return fail(fmt.Errorf("registry node %s is not joined and registry-capable", registryNode))
	}
	printTopology(out, topology, registryNode)
	log.line(fmt.Sprintf("derived topology: database tier %s (%d database nodes), registry on %s",
		topology.DatabaseTier, topology.Capable[layout.CapabilityDatabase], registryNode))

	// The mirror and the node pull credential are written at install time;
	// init verifies rather than mutates, so no k3s restart ever interrupts
	// the converge, and it verifies before the converge so a bad host file
	// never leaves a half-applied bundle behind.
	progress.Start("Enable embedded registry mirror")
	registries, err := runner.ReadFile(ctx, K3sRegistriesPath)
	if err != nil || !strings.Contains(string(registries), bundle.RegistryInternalHost) {
		return fail(fmt.Errorf("%s is missing the %s mirror; re-run install",
			K3sRegistriesPath, bundle.RegistryInternalHost))
	}
	pullSecret := registriesPullSecret(registries)
	if pullSecret == "" {
		return fail(fmt.Errorf("%s is missing the registry pull credential; "+
			"run skali cluster upgrade, which mints it for hosts installed "+
			"before the registry required authentication", K3sRegistriesPath))
	}
	progress.Skip("configured at install")

	authSecret, err := ensureAuthSecret(ctx, client)
	if err != nil {
		return fail(err)
	}
	tokenKeyPEM, tokenCertPEM, err := ensureRegistryTokenKeypair(ctx, client)
	if err != nil {
		return fail(err)
	}

	// The record published in-cluster carries the initialization inputs;
	// mutate the in-memory record first so the canonical text, the bundle
	// hash, and the eventual on-disk record all agree.
	record.Endpoints = &Endpoints{API: opts.Endpoints.API, Registry: opts.Endpoints.Registry, S3: opts.Endpoints.S3}
	record.TLS = &TLSConfig{IssuerEmail: opts.TLS.IssuerEmail, ACMEServer: opts.TLS.ACMEServer}
	record.RegistryNode = opts.RegistryNode
	record.Versions.Bundle = version.Version
	record.Versions.Installer = version.Version
	canonical, err := record.CanonicalYAML()
	if err != nil {
		return fail(err)
	}

	profile := bundle.Profile{
		SkalidImage:   opts.SkalidImage,
		SkalidImageID: opts.SkalidImageID,
		AuthSecret:    authSecret,
		RegistryHost:  bundle.RegistryInternalHost,
		Production: &bundle.Production{
			IngressHost:        opts.Endpoints.API,
			RegistryDomain:     opts.Endpoints.Registry,
			S3Domain:           opts.Endpoints.S3,
			TokenKeyPEM:        tokenKeyPEM,
			TokenCertPEM:       tokenCertPEM,
			NodePullSecret:     pullSecret,
			ACMEEmail:          opts.TLS.IssuerEmail,
			ACMEServer:         opts.TLS.ACMEServer,
			Capabilities:       layout.UnionCapabilities(live.Nodes),
			DatabaseTier:       topology.DatabaseTier,
			DatabaseStorage:    DefaultDatabaseStorage,
			RegistryStorage:    DefaultRegistryStorage,
			RegistryNode:       opts.RegistryNode,
			WebImage:           opts.WebImage,
			WebImageID:         opts.WebImageID,
			InstallationRecord: canonical,
		},
	}

	if err := bundle.Converge(ctx, client, profile, progress); err != nil {
		return fail(err)
	}

	progress.Start("Wait for skalid ready")
	if err := waitSkalidHealthy(ctx, client); err != nil {
		return fail(err)
	}
	progress.Done("")

	if !opts.SkipAdmin {
		email, password, err := opts.Admin(ctx)
		if err != nil {
			return fail(err)
		}
		adminProfile := profile
		adminProfile.AdminEmail = email
		adminProfile.AdminPassword = password
		if err := bundle.EnsureAdminUser(ctx, client, adminProfile, progress); err != nil {
			return fail(err)
		}
	}

	if err := SaveRecord(ctx, runner, record); err != nil {
		return fail(err)
	}
	if err := bundle.StampHash(ctx, client, profile); err != nil {
		return fail(err)
	}
	log.line("init complete")
	if err := log.flush(ctx); err != nil {
		return nil, err
	}
	return &InitResult{
		ConsoleURL:  "https://" + opts.Endpoints.API,
		APIURL:      "https://" + opts.Endpoints.API + "/api",
		RegistryURL: "https://" + opts.Endpoints.Registry,
		LogPath:     log.path,
	}, nil
}

// ValidateInitOptions is pure and is called by the CLI before a reconciled
// candidate is frozen. Invalid platform inputs therefore cannot start a
// topology operation.
func ValidateInitOptions(opts InitOptions) error {
	if opts.Endpoints.API == "" {
		return errors.New("init requires the api/ui domain")
	}
	if opts.Endpoints.Registry == "" {
		return errors.New("init requires the registry domain")
	}
	if opts.TLS.IssuerEmail == "" {
		return errors.New("init requires the tls issuer email")
	}
	if opts.SkalidImage == "" {
		return errors.New("init requires a skalid image")
	}
	if opts.WebImage == "" {
		return errors.New("init requires a web console image")
	}
	if opts.Admin == nil && !opts.SkipAdmin {
		return errors.New("init requires admin credentials")
	}
	domains := map[string]string{
		"api/ui": opts.Endpoints.API, "registry": opts.Endpoints.Registry,
	}
	if opts.Endpoints.S3 != "" {
		domains["s3"] = opts.Endpoints.S3
	}
	for label, domain := range domains {
		if strings.Contains(domain, "://") {
			return fmt.Errorf("%s domain %q must be a hostname without a protocol", label, domain)
		}
		if problems := validation.IsDNS1123Subdomain(domain); len(problems) > 0 {
			return fmt.Errorf("%s domain %q is invalid: %s",
				label, domain, strings.Join(problems, "; "))
		}
	}
	address, err := mail.ParseAddress(opts.TLS.IssuerEmail)
	if err != nil || address.Address != opts.TLS.IssuerEmail {
		return fmt.Errorf("tls issuer email %q is not a valid email address",
			opts.TLS.IssuerEmail)
	}
	if opts.TLS.ACMEServer != "" {
		parsed, err := url.Parse(opts.TLS.ACMEServer)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
			parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("acme server %q must be an HTTPS URL without credentials or fragment",
				opts.TLS.ACMEServer)
		}
	}
	return nil
}

// LayoutFromNodes rebuilds the installed layout from live node labels;
// init derives topology from it and never from a document on disk.
func LayoutFromNodes(nodes []corev1.Node, cluster string) layout.Layout {
	rebuilt := layout.Layout{
		Version: layout.CurrentVersion,
		Name:    cluster,
		Nodes:   make(map[string]layout.Node, len(nodes)),
	}
	for _, node := range nodes {
		rebuilt.Nodes[node.Name] = layout.Node{
			Role:         layout.RoleFromLabels(node.Labels),
			Capabilities: layout.CapabilitiesFromLabels(node.Labels),
		}
	}
	return rebuilt
}

// assertClusterMembership refuses nodes that were not stamped for this
// cluster, catching token or cluster-name typos before any converge.
func assertClusterMembership(nodes []corev1.Node, cluster string) error {
	var violations []string
	for _, node := range nodes {
		labeled, ok := node.Labels[layout.ClusterLabel]
		switch {
		case !ok:
			violations = append(violations,
				fmt.Sprintf("node %s has no %s label; it did not join through skali cluster",
					node.Name, layout.ClusterLabel))
		case labeled != cluster:
			violations = append(violations,
				fmt.Sprintf("node %s carries cluster label %q, expected %q; it joined a different installation",
					node.Name, labeled, cluster))
		}
	}
	if len(violations) > 0 {
		return errors.New("cluster membership check failed:\n  " + strings.Join(violations, "\n  "))
	}
	return nil
}

// assertLayout compares expected membership against the live layout and
// refuses on any difference, listing all of them.
func assertLayout(expected, live layout.Layout) error {
	var differences []string
	for _, name := range utils.SortedKeys(expected.Nodes) {
		node := expected.Nodes[name]
		liveNode, ok := live.Nodes[name]
		if !ok {
			differences = append(differences, fmt.Sprintf("node %s is expected but has not joined", name))
			continue
		}
		if node.Role != liveNode.Role {
			differences = append(differences,
				fmt.Sprintf("node %s: expected role %s, found %s", name, node.Role, liveNode.Role))
		}
		if !slices.Equal(normalizedCapabilities(node.Capabilities), normalizedCapabilities(liveNode.Capabilities)) {
			differences = append(differences,
				fmt.Sprintf("node %s: expected capabilities %s, found %s", name,
					strings.Join(node.Capabilities, ", "), strings.Join(liveNode.Capabilities, ", ")))
		}
	}
	for _, name := range utils.SortedKeys(live.Nodes) {
		if _, ok := expected.Nodes[name]; !ok {
			differences = append(differences, fmt.Sprintf("node %s has joined but is not in the layout", name))
		}
	}
	if len(differences) > 0 {
		return errors.New("the joined nodes do not match the asserted layout:\n  " +
			strings.Join(differences, "\n  "))
	}
	return nil
}

func normalizedCapabilities(capabilities []string) []string {
	normalized := append([]string(nil), capabilities...)
	slices.Sort(normalized)
	return normalized
}

// ensureAuthSecret reuses the existing cluster auth secret so repeat init
// stays convergent without persisting secrets host-side; a fresh cluster
// gets a new 32-byte value.
func ensureAuthSecret(ctx context.Context, client *kube.Client) (string, error) {
	secret, err := client.Clientset.CoreV1().Secrets(bundle.Namespace).Get(ctx, "skali-auth", metav1.GetOptions{})
	if err == nil {
		if value := secret.Data["AUTH_SECRET"]; len(value) > 0 {
			return string(value), nil
		}
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("read auth secret: %w", err)
	}
	value, err := utils.RandomToken(32)
	if err != nil {
		return "", fmt.Errorf("generate auth secret: %w", err)
	}
	return value, nil
}

// ensureRegistryTokenKeypair reuses the registry token signing keypair
// from the cluster so repeat init stays convergent and rotation stays an
// explicit later operation; a fresh cluster gets a new keypair.
func ensureRegistryTokenKeypair(ctx context.Context, client *kube.Client) (keyPEM, certPEM string, err error) {
	secret, err := client.Clientset.CoreV1().Secrets(bundle.Namespace).Get(ctx, "skali-registry-token", metav1.GetOptions{})
	if err == nil {
		key, cert := secret.Data["key.pem"], secret.Data["cert.pem"]
		if len(key) > 0 && len(cert) > 0 {
			return string(key), string(cert), nil
		}
	} else if !apierrors.IsNotFound(err) {
		return "", "", fmt.Errorf("read registry token secret: %w", err)
	}
	keyBytes, certBytes, err := registrytoken.GenerateSigningKeypair()
	if err != nil {
		return "", "", err
	}
	return string(keyBytes), string(certBytes), nil
}

// waitSkalidHealthy proves skalid up through the service proxy: no DNS,
// no TLS, no edge dependency (certificates may still be pending).
func waitSkalidHealthy(ctx context.Context, client *kube.Client) error {
	deadline := time.Now().Add(5 * time.Minute)
	var lastErr error
	for {
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("skalid never became healthy: %w", lastErr)
			}
			return errors.New("skalid never became healthy")
		}
		result := client.Clientset.CoreV1().Services(bundle.Namespace).
			ProxyGet("http", "skalid", "80", "/healthz", nil)
		_, err := result.DoRaw(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func firstCapableNode(l layout.Layout, capability string) string {
	names := make([]string, 0, len(l.Nodes))
	for name, node := range l.Nodes {
		if slices.Contains(node.Capabilities, capability) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(none)"
	}
	return names[0]
}

func printLayout(out io.Writer, l layout.Layout) {
	fmt.Fprintln(out, "cluster layout")
	names := make([]string, 0, len(l.Nodes))
	nameWidth := len("NODE")
	for name := range l.Nodes {
		names = append(names, name)
		nameWidth = max(nameWidth, len(name))
	}
	sort.Strings(names)
	fmt.Fprintf(out, "  %-*s  %-6s  %s\n", nameWidth, "NODE", "ROLE", "CAPABILITIES")
	for _, name := range names {
		node := l.Nodes[name]
		fmt.Fprintf(out, "  %-*s  %-6s  %s\n", nameWidth, name, node.Role,
			strings.Join(node.Capabilities, ", "))
	}
	fmt.Fprintln(out)
}

func printTopology(out io.Writer, topology layout.Topology, registryNode string) {
	fmt.Fprintln(out, "derived topology")
	databaseNodes := topology.Capable[layout.CapabilityDatabase]
	noun := "database nodes"
	if databaseNodes == 1 {
		noun = "database node"
	}
	fmt.Fprintf(out, "  database availability tier  %s (%d %s)\n", topology.DatabaseTier, databaseNodes, noun)
	fmt.Fprintf(out, "  registry placement          %s (installer-owned volume)\n", registryNode)
	fmt.Fprintln(out)
}

func compactLayout(l layout.Layout) string {
	names := make([]string, 0, len(l.Nodes))
	for name := range l.Nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		node := l.Nodes[name]
		parts = append(parts, fmt.Sprintf("%s(%s: %s)", name, node.Role,
			strings.Join(node.Capabilities, "+")))
	}
	return strings.Join(parts, ", ")
}

// initLog accumulates step lines and flushes them to a per-run file under
// LogDir through the runner; the full-content flush at step boundaries
// stays portable to remote runners.
type initLog struct {
	runner host.Runner
	path   string
	lines  []string
}

func newInitLog(ctx context.Context, runner host.Runner) (*initLog, error) {
	if err := runner.MkdirAll(ctx, LogDir, 0o750); err != nil {
		return nil, fmt.Errorf("create %s: %w", LogDir, err)
	}
	log := &initLog{
		runner: runner,
		path:   fmt.Sprintf("%s/init-%s.log", LogDir, time.Now().UTC().Format("20060102-150405")),
	}
	log.line("skali cluster init " + version.Version)
	return log, log.flush(ctx)
}

func (l *initLog) line(text string) {
	l.lines = append(l.lines, time.Now().UTC().Format(time.RFC3339)+" "+text)
}

func (l *initLog) flush(ctx context.Context) error {
	content := strings.Join(l.lines, "\n") + "\n"
	return l.runner.WriteFile(ctx, l.path, []byte(content), 0o600)
}

// loggingProgress mirrors progress events into the init log while
// delegating to the interactive renderer.
type loggingProgress struct {
	inner   Progress
	log     *initLog
	current string
}

func (p *loggingProgress) Start(title string) {
	p.current = title
	p.log.line("start: " + title)
	p.inner.Start(title)
}

func (p *loggingProgress) Done(detail string) {
	line := "ok: " + p.current
	if detail != "" {
		line += " (" + detail + ")"
	}
	p.log.line(line)
	p.log.flush(context.Background())
	p.inner.Done(detail)
}

func (p *loggingProgress) Skip(detail string) {
	line := "skip: " + p.current
	if detail != "" {
		line += " (" + detail + ")"
	}
	p.log.line(line)
	p.log.flush(context.Background())
	p.inner.Skip(detail)
}

func (p *loggingProgress) Note(line string) {
	p.log.line("note: " + line)
	p.inner.Note(line)
}
