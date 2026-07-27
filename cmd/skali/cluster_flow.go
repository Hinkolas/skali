package main

import (
	"bufio"
	"context"
	"errors"
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
// conversation: seed identity and capabilities, the k3s/coordinator
// bootstrap, and the offer to initialize immediately. Platform domains,
// image, TLS, and admin credentials are intentionally deferred to init.
func runInteractiveFreshFlow(ctx context.Context, out *os.File) error {
	return runInteractiveFreshFlowMode(ctx, out, false)
}

func runInteractiveCreateFlow(ctx context.Context, out *os.File) error {
	return runInteractiveFreshFlowMode(ctx, out, true)
}

func runInteractiveFreshFlowMode(ctx context.Context, out *os.File, seedOnly bool) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprintln(out, "This host is not part of a Skali installation. Install one?")
	fmt.Fprintln(out)

	if err := runDarwinVMPrompts(ctx, out, reader); err != nil {
		return err
	}
	if !seedOnly {
		installation, err := promptSession(out, reader).Select(ctx, cliprompt.SelectOptions{
			Title: "How should this host join Skali?",
			Options: []cliprompt.Option{
				{
					Label:       "Create a new cluster",
					Description: "start the first server on this host",
					Value:       "create",
				},
				{
					Label:       "Join an existing cluster",
					Description: "use an enrollment or join token",
					Value:       "join",
				},
			},
			DefaultValue: "create",
		})
		if err != nil {
			return err
		}
		if installation == "join" {
			return runInteractiveJoinFlow(ctx, out, reader)
		}
	}

	cluster, err := cliprompt.LineDefault(reader,
		"  cluster name ["+installer.DefaultCluster+"]: ", installer.DefaultCluster)
	if err != nil {
		return err
	}
	capabilities, err := promptCapabilities(ctx, out, reader)
	if err != nil {
		return err
	}
	network, err := promptNodeNetwork(ctx, out, reader)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)

	opts := installer.InstallOptions{
		Cluster:      cluster,
		Capabilities: capabilities,
		Network:      network,
		Management:   installer.ManagementReconciled,
	}
	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	opts.Progress = progress
	hostdBinary, _, err := loadHostdBinary()
	if err != nil {
		progress.Abort()
		return err
	}
	if err := applyDarwinInstallOptions(ctx, &opts); err != nil {
		progress.Abort()
		return err
	}
	record, err := installer.Install(ctx, runner(), opts)
	if err != nil {
		progress.Abort()
		return err
	}
	progress.Start("Bootstrap cluster coordinator")
	if err := bootstrapReconciledSeed(ctx, record, hostdBinary); err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	warnings := finishDarwinInstall(ctx, progress)
	progress.Done("")
	printWarnings(out, warnings)

	fmt.Fprintln(out)
	initialize, err := promptSession(out, reader).Confirm(ctx, cliprompt.ConfirmOptions{
		Title:   "This is the only node so far. Initialize Skali now?",
		Default: true,
	})
	if err != nil {
		return err
	}
	if !initialize {
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
func runInteractiveJoinFlow(ctx context.Context, out *os.File, reader *bufio.Reader) error {
	tokenFile, err := cliprompt.Line(reader, "  join token file path (empty to paste the token): ")
	if err != nil {
		return err
	}
	token := ""
	if tokenFile == "" {
		token, err = cliprompt.Secret(reader, "  join token: ")
		if err != nil {
			return err
		}
	} else {
		data, readErr := os.ReadFile(tokenFile)
		if readErr != nil {
			return fmt.Errorf("read join token file %s: %w", tokenFile, readErr)
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return fmt.Errorf("join token file %s is empty", tokenFile)
		}
	}
	claims, err := installer.InspectJoinToken(token)
	if reconciledToken(token) {
		server, promptErr := cliprompt.Line(reader,
			"  coordinator (host, host:port, or https URL): ")
		if promptErr != nil {
			return promptErr
		}
		capabilities, promptErr := promptCapabilities(ctx, out, reader)
		if promptErr != nil {
			return promptErr
		}
		network, promptErr := promptNodeNetwork(ctx, out, reader)
		if promptErr != nil {
			return promptErr
		}
		fmt.Fprintln(out)
		record, enrollErr := runReconciledEnrollment(ctx, reconciledEnrollmentOptions{
			Server: server, Token: token, Capabilities: capabilities, Network: network,
		})
		if enrollErr != nil {
			return enrollErr
		}
		fmt.Fprintf(out, "This host is enrolled in cluster %q as %s and is pending apply.\n",
			record.Cluster, record.Node.Role)
		fmt.Fprintln(out, "No k3s files or services were installed. Run `skali cluster plan` and")
		fmt.Fprintln(out, "`skali cluster apply` on an active server.")
		return nil
	}
	if err != nil {
		return err
	}

	role := claims.Role
	if role == "" {
		role, err = promptSession(out, reader).Select(ctx, cliprompt.SelectOptions{
			Title: "Which Kubernetes role should this node use?",
			Options: []cliprompt.Option{
				{Label: "Agent", Description: "runs workloads only", Value: layout.RoleAgent},
				{Label: "Server", Description: "joins the control plane", Value: layout.RoleServer},
			},
			DefaultValue: layout.RoleAgent,
		})
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "  role: %s (from token)\n", role)
	}
	cluster := claims.Cluster
	if cluster == "" {
		cluster, err = cliprompt.LineDefault(reader,
			"  cluster name ["+installer.DefaultCluster+"]: ", installer.DefaultCluster)
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "  cluster: %s (from token)\n", cluster)
	}
	server := claims.Server
	if server == "" {
		server, err = cliprompt.Line(reader, "  server url (https://<server>:6443): ")
	} else {
		server, err = cliprompt.LineDefault(reader, "  server url ["+server+"]: ", server)
	}
	if err != nil {
		return err
	}
	capabilities, err := promptCapabilities(ctx, out, reader)
	if err != nil {
		return err
	}
	network, err := promptNodeNetwork(ctx, out, reader)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	opts := installer.InstallOptions{
		Cluster:      cluster,
		Role:         role,
		Capabilities: capabilities,
		Network:      network,
		Join:         &installer.JoinOptions{Server: server, Token: token},
		Progress:     progress,
	}
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
func promptCapabilities(ctx context.Context, out *os.File, reader *bufio.Reader) ([]string, error) {
	options := make([]cliprompt.Option, 0, len(layout.Capabilities))
	for _, capability := range layout.Capabilities {
		options = append(options, cliprompt.Option{
			Label: capability,
			Value: capability,
		})
	}
	return promptSession(out, reader).MultiSelect(ctx, cliprompt.MultiSelectOptions{
		Title:         "What should this node run?",
		Description:   "Capabilities control workload placement.",
		Options:       options,
		DefaultValues: append([]string(nil), layout.Capabilities...),
		Validate: func(values []string) error {
			if len(values) == 0 {
				return errors.New("select at least one capability")
			}
			return nil
		},
	})
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
	var imageTar []byte
	if imageTarFlag != "" {
		var err error
		imageTar, opts.SkalidImage, opts.SkalidImageID, err =
			loadImageTar(ctx, imageTarFlag)
		if err != nil {
			return err
		}
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	opts.Progress = progress
	// The engine settles every running task before it asks for the admin
	// account, so prompting here never interleaves with the task printer.
	opts.Admin = func(ctx context.Context) (string, string, error) {
		fmt.Fprintln(out)
		return promptAdmin(reader)
	}
	if err := installer.ValidateInitOptions(opts); err != nil {
		progress.Abort()
		return err
	}
	if len(imageTar) > 0 {
		if err := importImageTar(ctx, runner(), imageTar, opts.SkalidImage, progress); err != nil {
			progress.Abort()
			return err
		}
	}

	if err := stageReconciledLayout(ctx, record, asserted); err != nil {
		progress.Abort()
		return err
	}
	prepared, err := prepareReconciledInit(ctx, record, progress)
	if err != nil {
		progress.Abort()
		return err
	}
	if prepared != nil {
		opts.RegistryNode = prepared.RegistryNode
	}
	result, err := installer.Init(ctx, runner(), record, opts)
	if err != nil {
		progress.Abort()
		return err
	}
	if err := finishReconciledInit(ctx, prepared); err != nil {
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
