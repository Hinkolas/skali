package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// releaseVersionPattern matches release-shaped installer versions (vX.Y.Z,
// no prerelease or dev suffix); only those have a published skalid image.
var releaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// runInteractiveFreshFlow drives the transcript's fresh single-node
// conversation: role and capability prompts, the api/ui domain and TLS
// issuer email, the k3s install, and the offer to initialize immediately.
func runInteractiveFreshFlow(ctx context.Context, out *os.File) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprintln(out, "This host is not part of a Skali installation. Install one?")
	fmt.Fprintln(out)

	if err := runDarwinVMPrompts(ctx, out, reader); err != nil {
		return err
	}
	role, err := cliprompt.Select(reader, out, "  role: ",
		[]string{"server (creates or extends a cluster)", "agent (joins an existing cluster)"}, 0)
	if err != nil {
		return err
	}
	if role == 1 {
		return runInteractiveJoinFlow(ctx, out, reader, layout.RoleAgent)
	}
	if !cliprompt.ConfirmDefaultYes(reader, "  first server (creates a new cluster)? [Y/n] ") {
		return runInteractiveJoinFlow(ctx, out, reader, layout.RoleServer)
	}

	cluster, err := cliprompt.LineDefault(reader,
		"  cluster name ["+installer.DefaultCluster+"]: ", installer.DefaultCluster)
	if err != nil {
		return err
	}
	capabilities, err := promptCapabilities(reader)
	if err != nil {
		return err
	}
	apiDomain, err := cliprompt.Line(reader, "  api/ui domain (for example skali.example.com): ")
	if err != nil {
		return err
	}
	registryDomain := ""
	if apiDomain != "" {
		defaultRegistry := registryDomainDefault(apiDomain)
		registryDomain, err = cliprompt.LineDefault(reader,
			"  registry domain ["+defaultRegistry+"]: ", defaultRegistry)
		if err != nil {
			return err
		}
	}
	issuerEmail, err := cliprompt.Line(reader, "  tls issuer email: ")
	if err != nil {
		return err
	}
	fmt.Fprintln(out)

	opts := installer.InstallOptions{
		Cluster:      cluster,
		Capabilities: capabilities,
	}
	if apiDomain != "" {
		opts.Endpoints = &installer.Endpoints{API: apiDomain, Registry: registryDomain}
	}
	if issuerEmail != "" {
		opts.TLS = &installer.TLSConfig{IssuerEmail: issuerEmail}
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	opts.Progress = progress
	if err := applyDarwinInstallOptions(ctx, &opts); err != nil {
		progress.Abort()
		return err
	}
	record, err := installer.Install(ctx, runner(), opts)
	if err != nil {
		progress.Abort()
		return err
	}
	warnings := finishDarwinInstall(ctx, progress)
	progress.Done("")
	printWarnings(out, warnings)

	fmt.Fprintln(out)
	if !cliprompt.ConfirmDefaultYes(reader, "This is the only node so far. Initialize Skali now? [Y/n] ") {
		fmt.Fprintln(out, "Run `skali cluster init` on this node once every planned node has joined.")
		return nil
	}
	fmt.Fprintln(out)
	return runInteractiveInit(ctx, out, reader, record, nil)
}

// runInteractiveJoinFlow enrolls this host into an existing cluster in
// the given role: the server URL and token come from `skali cluster
// token` run on a server. The token can be pasted directly so no file has
// to be staged for an interactive join.
func runInteractiveJoinFlow(ctx context.Context, out *os.File, reader *bufio.Reader, role string) error {
	cluster, err := cliprompt.LineDefault(reader,
		"  cluster name ["+installer.DefaultCluster+"]: ", installer.DefaultCluster)
	if err != nil {
		return err
	}
	capabilities, err := promptCapabilities(reader)
	if err != nil {
		return err
	}
	server, err := cliprompt.Line(reader, "  server url (https://<first-server>:6443): ")
	if err != nil {
		return err
	}
	join := &installer.JoinOptions{Server: server}
	join.TokenFile, err = cliprompt.Line(reader, "  join token file path (empty to paste the token): ")
	if err != nil {
		return err
	}
	if join.TokenFile == "" {
		join.Token, err = cliprompt.Secret(reader, "  join token: ")
		if err != nil {
			return err
		}
	}
	fmt.Fprintln(out)

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	opts := installer.InstallOptions{
		Cluster:      cluster,
		Role:         role,
		Capabilities: capabilities,
		Join:         join,
		Progress:     progress,
	}
	// On darwin this also reads a typed token file path on the Mac side;
	// the engine would look for it inside the VM.
	if err := applyDarwinInstallOptions(ctx, &opts); err != nil {
		progress.Abort()
		return err
	}
	_, err = installer.Install(ctx, runner(), opts)
	if err != nil {
		progress.Abort()
		return err
	}
	warnings := finishDarwinInstall(ctx, progress)
	progress.Done("")
	warnings = append(warnings, serverCountWarning(ctx, role)...)
	printWarnings(out, warnings)

	fmt.Fprintln(out)
	fmt.Fprintf(out, "This node has joined cluster %q as a %s. Run skali cluster init on a "+
		"server once every planned node has joined.\n", cluster, role)
	return nil
}

// promptCapabilities asks for a capability list, defaulting to all.
func promptCapabilities(reader *bufio.Reader) ([]string, error) {
	prompt := "  capabilities (" + strings.Join(layout.Capabilities, ", ") + ") [all]: "
	answer, err := cliprompt.Line(reader, prompt)
	if err != nil {
		return nil, err
	}
	if answer == "" || answer == "all" {
		return append([]string(nil), layout.Capabilities...), nil
	}
	var capabilities []string
	for part := range strings.SplitSeq(answer, ",") {
		capability := strings.TrimSpace(part)
		if capability == "" {
			continue
		}
		capabilities = append(capabilities, capability)
	}
	if len(capabilities) == 0 {
		return append([]string(nil), layout.Capabilities...), nil
	}
	return capabilities, nil
}

// runInteractiveInit initializes the cluster, defaulting from the record
// where install already gathered answers, and prompting for the admin
// account only after skalid is ready (the engine calls Admin at exactly
// that point).
func runInteractiveInit(ctx context.Context, out *os.File, reader *bufio.Reader,
	record *installer.Record, asserted *layout.Layout) error {
	opts := installer.InitOptions{Out: out, Layout: asserted}
	if err := seedInitInputs(reader, true, record, &opts); err != nil {
		return err
	}
	if err := resolveSkalidImage(reader, true, &opts); err != nil {
		return err
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	opts.Progress = progress
	if imageTarFlag != "" {
		var err error
		opts.SkalidImage, opts.SkalidImageID, err = stageSkalidImage(ctx, runner(), imageTarFlag, progress)
		if err != nil {
			progress.Abort()
			return err
		}
	}
	// The engine settles every running task before it asks for the admin
	// account, so prompting here never interleaves with the task printer.
	opts.Admin = func(ctx context.Context) (string, string, error) {
		fmt.Fprintln(out)
		return promptAdmin(reader)
	}

	result, err := installer.Init(ctx, runner(), record, opts)
	if err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	printInitReady(out, result)
	return nil
}

// seedInitInputs seeds the init endpoints and TLS inputs from the record
// where install or a previous init already gathered answers, and prompts
// only for fields the record does not carry (fields added after the
// recording installer's release stay empty on old records). When prompting
// is not allowed, a missing field is an error naming it.
func seedInitInputs(reader *bufio.Reader, promptAllowed bool,
	record *installer.Record, opts *installer.InitOptions) error {
	if record.Endpoints != nil {
		opts.Endpoints = *record.Endpoints
	}
	if record.TLS != nil {
		opts.TLS = *record.TLS
	}
	var err error
	if opts.Endpoints.API == "" {
		if !promptAllowed {
			return missingRecordField("api/ui domain")
		}
		opts.Endpoints.API, err = cliprompt.Line(reader, "  api/ui domain: ")
		if err != nil {
			return err
		}
	}
	if opts.Endpoints.Registry == "" {
		if !promptAllowed {
			return missingRecordField("registry domain")
		}
		defaultRegistry := registryDomainDefault(opts.Endpoints.API)
		opts.Endpoints.Registry, err = cliprompt.LineDefault(reader,
			"  registry domain ["+defaultRegistry+"]: ", defaultRegistry)
		if err != nil {
			return err
		}
	}
	if opts.TLS.IssuerEmail == "" {
		if !promptAllowed {
			return missingRecordField("tls issuer email")
		}
		opts.TLS.IssuerEmail, err = cliprompt.Line(reader, "  tls issuer email: ")
		if err != nil {
			return err
		}
	}
	return nil
}

func missingRecordField(field string) error {
	return fmt.Errorf("the installation record is missing the %s; "+
		"run skali cluster upgrade interactively to provide it", field)
}

// resolveSkalidImage settles the control-plane image when no tar stages
// it: released installers default to the published image of the same
// version, dev builds have no published default and must name one.
func resolveSkalidImage(reader *bufio.Reader, promptAllowed bool, opts *installer.InitOptions) error {
	var err error
	switch {
	case imageTarFlag != "":
		// A staged tar names the image itself; no prompt.
	case releaseVersionPattern.MatchString(versionpkg.Version):
		// A released installer has a published skalid image of the same
		// version; the operator can still type any other reference.
		defaultImage := "ghcr.io/hinkolas/skalid:" + versionpkg.Version
		if !promptAllowed {
			opts.SkalidImage = defaultImage
			return nil
		}
		opts.SkalidImage, err = cliprompt.LineDefault(reader,
			"  skalid image ["+defaultImage+"]: ", defaultImage)
	default:
		if !promptAllowed {
			return fmt.Errorf("no published skalid image exists for installer version %s; "+
				"pass --image-tar or run interactively", versionpkg.Version)
		}
		opts.SkalidImage, err = cliprompt.Line(reader, "  skalid image (dev build, no published default): ")
	}
	return err
}

// promptAdmin collects the first operator account credentials with a
// confirmation re-entry; they are never persisted host-side.
func promptAdmin(reader *bufio.Reader) (string, string, error) {
	email, err := cliprompt.Line(reader, "  admin email     : ")
	if err != nil {
		return "", "", err
	}
	for {
		password, err := cliprompt.Secret(reader, "  admin password  : ")
		if err != nil {
			return "", "", err
		}
		if password == "" {
			fmt.Fprintln(os.Stderr, "the password must not be empty")
			continue
		}
		repeat, err := cliprompt.Secret(reader, "  repeat password : ")
		if err != nil {
			return "", "", err
		}
		if password == repeat {
			return email, password, nil
		}
		fmt.Fprintln(os.Stderr, "the passwords do not match; try again")
	}
}

func printInitReady(out *os.File, result *installer.InitResult) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Skali is ready:")
	fmt.Fprintf(out, "  %-32s api/ui\n", result.APIURL)
	fmt.Fprintf(out, "  %-32s managed registry\n", result.RegistryURL)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Install logs:", result.LogPath)
}

// registryDomainDefault derives the registry domain suggestion from the
// api/ui domain by replacing its first label: skali.example.com suggests
// registry.example.com. A domain too short to strip is prefixed whole.
func registryDomainDefault(apiDomain string) string {
	if apiDomain == "" {
		return ""
	}
	labels := strings.Split(apiDomain, ".")
	if len(labels) >= 3 {
		return "registry." + strings.Join(labels[1:], ".")
	}
	return "registry." + apiDomain
}
