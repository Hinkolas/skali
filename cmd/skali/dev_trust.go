package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/Hinkolas/skali/internal/truststore"
)

func newDevTrustCommand() *cobra.Command {
	var checkOnly bool
	command := &cobra.Command{
		Use:   "trust",
		Short: "Trust the local platform's development CA in this machine's browsers",
		Long: "The local platform serves every route over https from a development CA\n" +
			"generated for this installation. skali itself trusts it; browsers need it\n" +
			"in the OS trust store. skali dev offers this once after creating the\n" +
			"platform; this command installs it (again) or, with --check, only reports\n" +
			"where it is trusted. Installing is an addition of one certificate and safe\n" +
			"to repeat; macOS asks for the login password, Linux for sudo.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runDevTrust(command.Context(), command.OutOrStdout(), checkOnly)
		},
	}
	command.Flags().BoolVar(&checkOnly, "check", false, "report the trust state without installing anything")
	return command
}

func runDevTrust(ctx context.Context, out io.Writer, checkOnly bool) error {
	ca, err := localdev.LoadCA()
	if errors.Is(err, localdev.ErrNotInstalled) {
		return errors.New("the local platform is not installed; run skali dev first")
	}
	if err != nil {
		return err
	}
	certificate := trustCertificate(ca)
	var status truststore.Status
	if checkOnly {
		status, err = truststore.Check(ctx, certificate)
	} else {
		status, err = truststore.Install(ctx, certificate)
	}
	if err != nil {
		return err
	}
	style := clirender.StyleFor(out)
	printTrustStatus(out, style, status, ca)
	if !status.Trusted() {
		if checkOnly {
			return errors.New("the development CA is not trusted; run skali dev trust")
		}
		return errors.New("the development CA could not be trusted everywhere; import " + ca.CertPath + " by hand where it says so")
	}
	return nil
}

func trustCertificate(ca *localdev.CA) truststore.Certificate {
	return truststore.Certificate{Path: ca.CertPath, PEM: ca.CertPEM, Name: ca.CommonName}
}

// printTrustStatus lists every store with its verdict, then the CA file.
func printTrustStatus(out io.Writer, style *clirender.Style, status truststore.Status, ca *localdev.CA) {
	width := 0
	for _, store := range status.Stores {
		width = max(width, len(store.Name))
	}
	for _, store := range status.Stores {
		var glyph, verdict string
		switch store.State {
		case truststore.StateTrusted:
			glyph, verdict = style.Check(), style.Green("trusted")
		case truststore.StateUnavailable:
			glyph, verdict = style.Yellow("- "), style.Yellow("unavailable")
		default:
			glyph, verdict = style.Cross(), style.Red(string(store.State))
		}
		line := fmt.Sprintf("%s%-*s  %s", glyph, width, store.Name, verdict)
		if store.Detail != "" {
			line += "  " + style.Dim(store.Detail)
		}
		fmt.Fprintln(out, line)
	}
	fmt.Fprintf(out, "%s %s\n", style.Dim("CA certificate:"), ca.CertPath)
	fmt.Fprintf(out, "%s %s\n", style.Dim("subject:       "), ca.CommonName)
}

// maybeTrustCA runs after a successful skali dev login: when the
// development CA is not yet trusted by this machine's browsers and a
// person is present, it installs it once (macOS asks for the login
// password), remembering the attempt so a declined dialog never nags
// again. Otherwise it points at skali dev trust. Never fatal: the
// platform works for skali either way.
func maybeTrustCA(ctx context.Context, out io.Writer, state *localdev.State) {
	ca, err := localdev.LoadCA()
	if err != nil {
		return
	}
	certificate := trustCertificate(ca)
	status, err := truststore.Check(ctx, certificate)
	if err == nil && status.Trusted() {
		return
	}
	style := clirender.StyleFor(out)
	if err == nil && cliprompt.Interactive() && !state.CATrustAttempted {
		fmt.Fprintln(out, "Trusting the development CA so browsers accept https://*.localhost."+
			trustPromptHint())
		tasks := clirender.NewTasks(out)
		task := tasks.Start("Trust development CA")
		status, err = truststore.Install(ctx, certificate)
		state.CATrustAttempted = true
		_ = localdev.SaveState(state)
		if err == nil && status.Trusted() {
			task.Done(ca.CommonName)
			return
		}
		task.Fail()
		if err != nil {
			fmt.Fprintf(out, "  %s\n", style.Yellow("warning: "+err.Error()))
		} else {
			printTrustStatus(out, style, status, ca)
		}
	}
	fmt.Fprintf(out, "  %s\n", style.Yellow("warning: browsers on this machine do not trust the development CA yet; "+
		"run skali dev trust, or import "+ca.CertPath+" yourself"))
}

// trustPromptHint names the credential the OS will ask for.
func trustPromptHint() string {
	switch truststore.OS() {
	case "darwin":
		return " macOS will ask for your login password."
	case "linux":
		return " sudo may ask for your password."
	}
	return ""
}
