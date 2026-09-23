package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
)

// newRouteCommand groups the public-route views of one environment: what
// each route serves and whether its domain reaches the installation.
func newRouteCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "route",
		Short: "List the environment's routes and probe their domains",
		Long: "Routes are the public hostnames of an environment's applications. list\n" +
			"shows each route with its certificate and the reconciler's last verdict\n" +
			"on whether the domain reaches this installation; probe checks the\n" +
			"domains right now, address by address, and asks the reconciler to act\n" +
			"on the result (a domain that arrived triggers a fresh certificate\n" +
			"issuance without waiting for the next pass).",
	}
	var remote, environment string
	command.PersistentFlags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	command.PersistentFlags().StringVar(&environment, "environment", "",
		"environment name (default: the checkout binding)")

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the environment's routes with their certificate and edge verdict",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(command.Context(), start, environment, remote)
			if err != nil {
				return err
			}
			status, err := target.api.EnvironmentStatus(command.Context(), target.environmentID)
			if err != nil {
				return err
			}
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			printHeader(out, style, headerRow{label: "environment", value: target.environment, note: target.remoteName})
			fmt.Fprintln(out)
			for _, line := range routeListLines(style, status, remoteReadySummary(target.remoteName).Edge) {
				fmt.Fprintln(out, line)
			}
			return nil
		},
	}
	probe := &cobra.Command{
		Use:   "probe",
		Short: "Check right now whether the route domains reach this installation",
		Long: "Resolves every TLS route domain of the environment and asks each address\n" +
			"whether it answers as this installation. The verdicts replace the\n" +
			"reconciler's cached ones: a domain that arrived triggers a fresh\n" +
			"certificate issuance within the next pass, even while cert-manager is\n" +
			"waiting out a failure backoff.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(command.Context(), start, environment, remote)
			if err != nil {
				return err
			}
			probes, err := target.api.ProbeRoutes(command.Context(), target.environmentID)
			if err != nil {
				return err
			}
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			printHeader(out, style, headerRow{label: "environment", value: target.environment, note: target.remoteName})
			fmt.Fprintln(out)
			for _, line := range routeProbeLines(style, probes) {
				fmt.Fprintln(out, line)
			}
			return nil
		},
	}
	command.AddCommand(list, probe)
	return command
}

// routeListLines renders one row per route: service and key, the URL, the
// certificate state, and the edge verdict where the reconciler has one.
func routeListLines(style *clirender.Style, status *client.EnvironmentStatus, ports edgePorts) []string {
	var lines []string
	for _, service := range status.Services {
		for _, route := range service.Routes {
			line := fmt.Sprintf("%-24s  %s", service.Key+"/"+route.Key, style.Link(routeURL(route, ports)))
			switch {
			case route.Deferred():
				line += "  " + style.Yellow("cert deferred · domain not pointing here yet")
			case route.Certificate != nil && route.Certificate.State != "active":
				line += "  " + style.Dim("cert ") + route.Certificate.State
			case route.Certificate != nil:
				line += "  " + style.Dim("cert active")
			}
			if route.Edge != nil && route.Edge.State != "" {
				line += "  " + style.Dim("edge "+route.Edge.State)
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, style.Dim("no routes"))
	}
	return lines
}

// routeProbeLines renders one block per probed route: the verdict, its
// sentence, and one line per address.
func routeProbeLines(style *clirender.Style, probes []client.RouteProbe) []string {
	if len(probes) == 0 {
		return []string{style.Dim("no TLS routes to probe")}
	}
	var lines []string
	for i, probe := range probes {
		if i > 0 {
			lines = append(lines, "")
		}
		verdict := probe.Edge.State
		switch probe.Edge.State {
		case "reachable":
			verdict = style.Green("reachable")
		case "unknown":
			verdict = style.Dim("unknown")
		default:
			verdict = style.Yellow(probe.Edge.State)
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %s", probe.Service+"/"+probe.Key, probe.Domain, verdict))
		if probe.Edge.Message != "" {
			lines = append(lines, "  "+style.Dim(probe.Edge.Message))
		}
		for _, address := range probe.Edge.Addresses {
			lines = append(lines, "  "+edgeAddressLine(address))
		}
	}
	return lines
}

// edgeAddressLine mirrors the reconciler's journal wording for one probed
// address.
func edgeAddressLine(address client.EdgeAddress) string {
	var verdict string
	switch address.Outcome {
	case "ours":
		verdict = "answered by this installation"
	case "foreign":
		verdict = "answered by another server"
	default:
		verdict = "did not answer"
	}
	line := address.Address + ": " + verdict
	if detail := strings.TrimSpace(address.Detail); detail != "" {
		line += " (" + detail + ")"
	}
	return line
}
