package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/kube"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// existingClusterClient loads the client from the explicit kubeconfig,
// after enforcing the mode flag contract.
func existingClusterClient() (*kube.Client, error) {
	if err := validateExistingMode(); err != nil {
		return nil, err
	}
	return kube.New(kubeconfigFlag)
}

// runExistingInstall installs or reconverges the Skali bundle into the
// existing cluster. install and init collapse into this one step; a config
// drives it non-interactively, otherwise it prompts.
func runExistingInstall(ctx context.Context, out *os.File, reader *bufio.Reader, configPath string) error {
	client, err := existingClusterClient()
	if err != nil {
		return err
	}
	banner(out)
	detection, err := installer.DetectCluster(ctx, client)
	if err != nil {
		return err
	}
	if detection.State == installer.ClusterManaged {
		return errors.New("this cluster is managed by skali on its hosts; run skali cluster on a member node")
	}

	fmt.Fprintln(out, "mode: existing cluster (unmanaged hosts)")
	fmt.Fprintln(out, "  This mode installs and maintains only the Skali system bundle. Node")
	fmt.Fprintln(out, "  lifecycle, k3s, and Kubernetes upgrades remain yours.")

	var config *installer.ExistingClusterConfig
	var adminPassword string
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", configPath, err)
		}
		config, err = installer.ParseExistingClusterConfig(data)
		if err != nil {
			return err
		}
		password, err := os.ReadFile(config.Admin.PasswordFile)
		if err != nil {
			return fmt.Errorf("read admin password file %s: %w", config.Admin.PasswordFile, err)
		}
		adminPassword = strings.TrimSpace(string(password))
	} else {
		if !cliprompt.Interactive() {
			return errors.New("non-interactive run requires --config")
		}
		config, adminPassword, err = promptExistingConfig(ctx, out, reader)
		if err != nil {
			return err
		}
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	if err := installer.PreflightExistingCluster(ctx, client, config, progress); err != nil {
		progress.Abort()
		return err
	}
	record := installer.RecordFromExistingConfig(config, detection.Record)
	result, err := installer.ConvergeExistingCluster(ctx, client, record, installer.ExistingClusterOptions{
		SkalidImage:   config.Skalid.Image,
		SkalidImageID: config.Skalid.ImageID,
		AdminEmail:    config.Admin.Email,
		AdminPassword: adminPassword,
		Progress:      progress,
	})
	if err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Skali is ready:")
	fmt.Fprintf(out, "  %-32s api/ui\n", result.APIURL)
	fmt.Fprintf(out, "  %-32s managed registry\n", result.RegistryURL)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Application images pull through the registry domain; skalid injects a")
	fmt.Fprintln(out, "pull secret into each project namespace. The registry domain must be")
	fmt.Fprintln(out, "publicly resolvable and issuable for pulls to succeed.")
	return nil
}

// promptExistingConfig gathers the existing-cluster configuration
// interactively, mirroring the config schema.
func promptExistingConfig(ctx context.Context, out *os.File, reader *bufio.Reader) (*installer.ExistingClusterConfig, string, error) {
	config := &installer.ExistingClusterConfig{}
	var err error
	if config.Endpoints.API, err = cliprompt.Line(reader, "  api/ui domain: "); err != nil {
		return nil, "", err
	}
	defaultRegistry := registryDomainDefault(config.Endpoints.API)
	if config.Endpoints.Registry, err = cliprompt.LineDefault(reader,
		"  registry domain ["+defaultRegistry+"]: ", defaultRegistry); err != nil {
		return nil, "", err
	}
	if config.TLS.IssuerEmail, err = cliprompt.Line(reader, "  tls issuer email: "); err != nil {
		return nil, "", err
	}
	if config.Ingress.ClassName, err = cliprompt.Line(reader, "  ingress class: "); err != nil {
		return nil, "", err
	}
	if config.Storage.ClassName, err = cliprompt.Line(reader, "  storage class (empty for the cluster default): "); err != nil {
		return nil, "", err
	}
	if config.Database.Tier, err = promptSession(out, reader).Select(ctx, cliprompt.SelectOptions{
		Title:       "Database availability tier",
		Description: "Choose the bootstrap database replication mode.",
		Options: []cliprompt.Option{
			{Label: "single", Description: "one instance", Value: "single"},
			{Label: "asynchronous", Description: "replicated with asynchronous failover", Value: "asynchronous"},
			{Label: "synchronous", Description: "replicated with synchronous failover", Value: "synchronous"},
		},
		DefaultValue: "single",
	}); err != nil {
		return nil, "", err
	}
	if config.Skalid.Image, err = cliprompt.Line(reader, "  skalid image: "); err != nil {
		return nil, "", err
	}
	if config.Admin.Email, err = cliprompt.Line(reader, "  admin email: "); err != nil {
		return nil, "", err
	}
	password, err := promptAdminPassword(reader)
	if err != nil {
		return nil, "", err
	}
	// Route the gathered values through the strict parser so defaults and
	// validation are identical to the config-file path. Admin.PasswordFile
	// is not used interactively, so a placeholder keeps the parser happy.
	config.Admin.PasswordFile = "(interactive)"
	rendered, err := renderExistingConfigYAML(config)
	if err != nil {
		return nil, "", err
	}
	parsed, err := installer.ParseExistingClusterConfig(rendered)
	if err != nil {
		return nil, "", err
	}
	return parsed, password, nil
}

// promptAdminPassword collects and confirms the admin password.
func promptAdminPassword(reader *bufio.Reader) (string, error) {
	for {
		password, err := cliprompt.Secret(reader, "  admin password: ")
		if err != nil {
			return "", err
		}
		if password == "" {
			fmt.Fprintln(os.Stderr, "the password must not be empty")
			continue
		}
		repeat, err := cliprompt.Secret(reader, "  repeat password: ")
		if err != nil {
			return "", err
		}
		if password == repeat {
			return password, nil
		}
		fmt.Fprintln(os.Stderr, "the passwords do not match; try again")
	}
}

// runExistingStatus prints the existing-cluster installation's health.
func runExistingStatus(ctx context.Context, out *os.File) error {
	client, err := existingClusterClient()
	if err != nil {
		return err
	}
	banner(out)
	detection, err := installer.DetectCluster(ctx, client)
	if err != nil {
		return err
	}
	switch detection.State {
	case installer.ClusterInstalled:
	case installer.ClusterFresh:
		fmt.Fprintln(out, "no Skali bundle is installed in this cluster")
		fmt.Fprintln(out, "install with: skali cluster install --mode existing-cluster --kubeconfig <path> --config <file>")
		return nil
	case installer.ClusterManaged:
		return errors.New("this cluster is managed by skali on its hosts; run skali cluster on a member node")
	case installer.ClusterDamaged:
		return fmt.Errorf("the in-cluster installation record is damaged: %s", detection.Problem)
	}

	status, err := installer.GatherClusterStatus(ctx, client, detection.Record)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "cluster %q: existing-cluster Skali installation\n", detection.Record.Cluster)
	if status.BundleCurrent {
		fmt.Fprintf(out, "  bundle     %s (current)\n", status.BundleVersion)
	} else {
		fmt.Fprintf(out, "  bundle     %s (skali is %s)\n", status.BundleVersion, versionpkg.Version)
	}
	fmt.Fprintf(out, "  nodes      %d\n", status.Nodes)
	parts := make([]string, 0, len(status.Components))
	for _, component := range status.Components {
		if component.Healthy {
			parts = append(parts, component.Name+" healthy")
		} else {
			parts = append(parts, component.Name+" "+component.Detail)
		}
	}
	fmt.Fprintf(out, "  bootstrap  %s\n", strings.Join(parts, ", "))
	return nil
}

// runExistingUpgrade reconverges the bundle from the stored record.
func runExistingUpgrade(ctx context.Context, out *os.File) error {
	client, err := existingClusterClient()
	if err != nil {
		return err
	}
	banner(out)
	detection, err := installer.DetectCluster(ctx, client)
	if err != nil {
		return err
	}
	if detection.State != installer.ClusterInstalled {
		return fmt.Errorf("no existing-cluster Skali installation found (state %s)", detection.State)
	}
	if detection.Record.Existing == nil {
		return errors.New("the in-cluster record is missing existing-cluster fields; re-run install --config")
	}
	image, imageID, err := installer.RunningSkalidImage(ctx, client)
	if err != nil {
		return err
	}
	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	if _, err := installer.ConvergeExistingCluster(ctx, client, detection.Record, installer.ExistingClusterOptions{
		SkalidImage:   image,
		SkalidImageID: imageID,
		SkipAdmin:     true,
		Progress:      progress,
	}); err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "bundle reconverged to %s\n", versionpkg.Version)
	return nil
}

// runExistingUninstall removes the bundle; bundle scope only.
func runExistingUninstall(ctx context.Context, out *os.File, reader *bufio.Reader, scope, confirmName string) error {
	if scope != "" && scope != "bundle" {
		return fmt.Errorf("existing-cluster mode owns only the skali bundle; node lifecycle stays with the cluster operator")
	}
	client, err := existingClusterClient()
	if err != nil {
		return err
	}
	banner(out)
	detection, err := installer.DetectCluster(ctx, client)
	if err != nil {
		return err
	}
	if detection.State != installer.ClusterInstalled {
		return fmt.Errorf("no existing-cluster Skali installation found (state %s)", detection.State)
	}
	inventory, err := installer.GatherBundleInventory(ctx, client)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, installer.DescribeBundleRemoval(inventory))
	fmt.Fprintln(out, "Operators reused from the cluster (use-existing) are left in place.")
	fmt.Fprintln(out)
	confirmed, err := confirmCluster(ctx, out, reader, detection.Record.Cluster, confirmName)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("confirmation did not match the cluster name %q; nothing was removed", detection.Record.Cluster)
	}
	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	if err := installer.UninstallExistingClusterBundle(ctx, client, detection.Record, progress); err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	fmt.Fprintln(out, "\nThe Skali bundle is removed; the cluster is otherwise untouched.")
	return nil
}

// runExistingRoot is the bare `skali cluster --mode existing-cluster`
// entry: detect and either offer install (fresh) or a reduced menu
// (installed). Tier and repair are not offered: tier derives from a
// config field here (no node labels to count), and host-level repair is
// meaningless on a cluster skali does not administer.
func runExistingRoot(ctx context.Context, out *os.File) error {
	client, err := existingClusterClient()
	if err != nil {
		return err
	}
	banner(out)
	detection, err := installer.DetectCluster(ctx, client)
	if err != nil {
		return err
	}
	switch detection.State {
	case installer.ClusterManaged:
		return errors.New("this cluster is managed by skali on its hosts; run skali cluster on a member node")
	case installer.ClusterDamaged:
		return fmt.Errorf("the in-cluster installation record is damaged: %s", detection.Problem)
	case installer.ClusterFresh:
		fmt.Fprintln(out, "no Skali bundle is installed in this cluster")
		fmt.Fprintln(out, "install with: skali cluster install --mode existing-cluster --kubeconfig <path> --config <file>")
		return nil
	}

	// Interactive runs reach the status block through the menu; a
	// non-interactive run has no menu, so the block is the output.
	if !cliprompt.Interactive() {
		return runExistingStatus(ctx, out)
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		answer, err := promptSession(out, reader).Select(ctx, cliprompt.SelectOptions{
			Title: "What would you like to do?",
			Options: []cliprompt.Option{
				{Label: "Show status", Value: "status"},
				{Label: "Upgrade", Value: "upgrade"},
				{Label: "Uninstall", Value: "uninstall"},
				{Label: "Quit", Value: "quit"},
			},
			DefaultValue: "quit",
		})
		if err != nil {
			return nil
		}
		switch answer {
		case "status":
			if err := runExistingStatus(ctx, out); err != nil {
				return err
			}
		case "upgrade":
			return runExistingUpgrade(ctx, out)
		case "uninstall":
			return runExistingUninstall(ctx, out, reader, "", "")
		case "quit":
			return nil
		}
	}
}

// runExistingDiagnose runs the cluster-level diagnosis against the
// external kubeconfig; there is no host to probe here.
func runExistingDiagnose(ctx context.Context, out *os.File) error {
	client, err := existingClusterClient()
	if err != nil {
		return err
	}
	banner(out)
	detection, err := installer.DetectCluster(ctx, client)
	if err != nil {
		return err
	}
	if detection.State == installer.ClusterManaged {
		return errors.New("this cluster is managed by skali on its hosts; run skali cluster diagnose on a member node")
	}
	diagnosis := installer.DiagnoseCluster(ctx, client)
	fmt.Fprintln(out, "existing-cluster Skali installation")
	for _, check := range diagnosis.Checks {
		marker := "ok  "
		switch check.Severity {
		case installer.SeverityWarn:
			marker = "warn"
		case installer.SeverityFail:
			marker = "fail"
		}
		fmt.Fprintf(out, "  %s  %s: %s\n", marker, check.Name, check.Detail)
		for _, sub := range check.Sub {
			for line := range strings.SplitSeq(sub, "\n") {
				fmt.Fprintf(out, "          %s\n", strings.TrimPrefix(line, "  "))
			}
		}
	}
	if diagnosis.Fails() > 0 {
		return fmt.Errorf("diagnosis found %d problem(s)", diagnosis.Fails())
	}
	return nil
}

// renderExistingConfigYAML marshals a gathered config so the interactive
// path validates and defaults through the same strict parser the config
// file uses.
func renderExistingConfigYAML(config *installer.ExistingClusterConfig) ([]byte, error) {
	return yaml.Marshal(config)
}

// existingModeRefusal words the refusal for commands that never apply to
// an unmanaged cluster.
func existingModeRefusal(action string) error {
	return fmt.Errorf("existing-cluster mode never manages nodes; %s applies to skali-managed k3s clusters", action)
}
