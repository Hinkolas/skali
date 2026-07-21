package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
)

// runInteractiveFreshFlow drives the transcript's fresh single-node
// conversation: role and capability prompts, the api/ui domain and TLS
// issuer email, the k3s install, and the offer to initialize immediately.
func runInteractiveFreshFlow(ctx context.Context, out *os.File) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprintln(out, "This host is not part of a Skali installation. Install one?")
	fmt.Fprintln(out)

	role, err := cliprompt.Select(reader, out, "  role: ",
		[]string{"server (creates or extends a cluster)", "agent (joins an existing cluster)"}, 0)
	if err != nil {
		return err
	}
	if role == 1 {
		return errors.New("joining an existing cluster is not implemented in this slice; " +
			"multi-node enrollment arrives with a later milestone")
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
		opts.Endpoints = &installer.Endpoints{API: apiDomain}
	}
	if issuerEmail != "" {
		opts.TLS = &installer.TLSConfig{IssuerEmail: issuerEmail}
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	opts.Progress = progress
	record, err := installer.Install(ctx, runner(), opts)
	if err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")

	fmt.Fprintln(out)
	if !cliprompt.ConfirmDefaultYes(reader, "This is the only node so far. Initialize Skali now? [Y/n] ") {
		fmt.Fprintln(out, "Run `skali-installer init` on this node once every planned node has joined.")
		return nil
	}
	fmt.Fprintln(out)
	return runInteractiveInit(ctx, out, reader, record, nil)
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
	if record.Endpoints != nil {
		opts.Endpoints = *record.Endpoints
	}
	if record.TLS != nil {
		opts.TLS = *record.TLS
	}
	var err error
	if opts.Endpoints.API == "" {
		opts.Endpoints.API, err = cliprompt.Line(reader, "  api/ui domain: ")
		if err != nil {
			return err
		}
	}
	if opts.TLS.IssuerEmail == "" {
		opts.TLS.IssuerEmail, err = cliprompt.Line(reader, "  tls issuer email: ")
		if err != nil {
			return err
		}
	}
	opts.SkalidImage, err = cliprompt.Line(reader, "  skalid image (no published bootstrap images yet): ")
	if err != nil {
		return err
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

	result, err := installer.Init(ctx, runner(), record, opts)
	if err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	printInitReady(out, result)
	return nil
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
	fmt.Fprintln(out, "  managed registry: in-cluster only (public endpoint arrives with registry authentication)")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Install logs:", result.LogPath)
}
