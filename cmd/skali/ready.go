package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/Hinkolas/skali/internal/utils"
)

// readySummary configures the block printed under a green "ready": every
// public route as a clickable URL grouped by application, preceded on the
// local platform by the dashboard and annotated with the host dev process
// serving an intercepted application.
type readySummary struct {
	// Dashboard is the local console URL; empty on remotes, whose console
	// is the master URL the user already knows.
	Dashboard string
	// Edge is the host ports the edge is published on; zero ports mean
	// the scheme defaults and stay out of the URLs. Only the local platform
	// can map its edge to non-default host ports (the test-suite override).
	Edge edgePorts
	// DevPorts maps intercepted applications to their host ports by port
	// name; nil outside bare skali dev.
	DevPorts map[string]map[string]int
	// ProtectionBypassed marks a deploy that entered a promote-only
	// environment on the explicit bypass; the first summary row says so.
	ProtectionBypassed bool
}

// edgePorts are the host ports an edge is published on, for URLs.
type edgePorts struct {
	HTTP  int
	HTTPS int
}

// localEdgePorts are the local platform's edge ports.
func localEdgePorts() edgePorts {
	return edgePorts{HTTP: localdev.HTTPPort(), HTTPS: localdev.HTTPSPort()}
}

// remoteReadySummary is the summary for a deploy, promote, or rollback
// against a named remote: routes only, with the local platform's host
// ports when the remote is the dev-owned local one.
func remoteReadySummary(remoteName string) readySummary {
	if remoteName == localRemoteName {
		return readySummary{Edge: localEdgePorts()}
	}
	return readySummary{}
}

// printReadySummary reads the environment status and prints the summary.
// A failed status read degrades to a warning: the deployment already
// succeeded, and a missing route list must never turn that into an error.
func printReadySummary(ctx context.Context, out io.Writer, api *client.Client,
	environmentID string, summary readySummary) {
	style := clirender.StyleFor(out)
	status, err := api.EnvironmentStatus(ctx, environmentID)
	if err != nil {
		if summary.Dashboard != "" {
			fmt.Fprintf(out, "  %s  %s\n", style.Dim("dashboard"), style.Link(summary.Dashboard))
		}
		fmt.Fprintf(out, "  %s\n", style.Yellow("warning: could not load routes: "+err.Error()))
		return
	}
	for _, line := range readySummaryLines(style, status, summary) {
		fmt.Fprintln(out, line)
	}
	for _, line := range readyWarningLines(style, status) {
		fmt.Fprintf(out, "  %s\n", line)
	}
}

// readyWarningLines names every deferred route once per domain: the https
// link above will not answer until DNS moves, and the certificate issues
// itself once it does.
func readyWarningLines(style *clirender.Style, status *client.EnvironmentStatus) []string {
	seen := map[string]bool{}
	var lines []string
	for _, service := range status.Services {
		for _, route := range service.Routes {
			if !route.Deferred() || seen[route.Domain] {
				continue
			}
			seen[route.Domain] = true
			text := "warning: " + route.Domain + " does not reach this installation yet"
			if route.Edge.Message != "" {
				text += " (" + route.Edge.Message + ")"
			}
			text += "; TLS is issued automatically once its DNS points here"
			lines = append(lines, style.Yellow(text))
		}
	}
	return lines
}

// readySummaryLines renders the summary rows. Applications without routes
// and without a host dev process are omitted; a summary with no rows at
// all prints nothing, so an app-less deploy ends on the bare ready line.
func readySummaryLines(style *clirender.Style, status *client.EnvironmentStatus, summary readySummary) []string {
	type row struct {
		label string
		lines []string
	}
	var rows []row
	if summary.ProtectionBypassed {
		rows = append(rows, row{label: "protection", lines: []string{style.Yellow("bypassed (recorded on the run)")}})
	}
	if summary.Dashboard != "" {
		rows = append(rows, row{label: "dashboard", lines: []string{style.Link(summary.Dashboard)}})
	}
	seen := map[string]bool{}
	for _, service := range status.Services {
		if service.Type != "application" {
			continue
		}
		seen[service.Key] = true
		var lines []string
		for _, route := range service.Routes {
			lines = append(lines, routeReadyLine(style, route, summary.Edge))
		}
		if note := devProcessNote(style, summary.DevPorts[service.Key]); note != "" {
			if len(lines) == 0 {
				lines = append(lines, note)
			} else {
				lines[0] += "  " + note
			}
		}
		if len(lines) > 0 {
			rows = append(rows, row{label: service.Key, lines: lines})
		}
	}
	// An intercepted application the status does not list yet (an
	// attach to an in-flight run before its revision settled) still gets
	// its host port line.
	for _, key := range utils.SortedKeys(summary.DevPorts) {
		if seen[key] {
			continue
		}
		if note := devProcessNote(style, summary.DevPorts[key]); note != "" {
			rows = append(rows, row{label: key, lines: []string{note}})
		}
	}
	width := 0
	for _, entry := range rows {
		width = max(width, len(entry.label))
	}
	var lines []string
	for _, entry := range rows {
		for index, line := range entry.lines {
			label := strings.Repeat(" ", width)
			if index == 0 {
				label = style.Dim(entry.label) + strings.Repeat(" ", width-len(entry.label))
			}
			lines = append(lines, "  "+label+"  "+line)
		}
	}
	return lines
}

// routeReadyLine renders one route as its URL plus, while the certificate
// is not yet active, the certificate state so an https link that will not
// answer yet is not a surprise. A deferred route says so instead: its
// certificate waits for the domain, not for the issuer.
func routeReadyLine(style *clirender.Style, route client.RouteStatus, ports edgePorts) string {
	line := style.Link(routeURL(route, ports))
	switch {
	case route.Deferred():
		line += "  " + style.Dim("cert ") + style.Yellow("deferred · domain not pointing here yet")
	case route.Certificate != nil && route.Certificate.State != "active":
		detail := stateColor(style, route.Certificate.State)
		if route.Certificate.Reason != "" {
			detail += " (" + route.Certificate.Reason + ")"
		}
		line += "  " + style.Dim("cert ") + detail
	}
	return line
}

// routeURL builds the public URL of a route: https where a certificate
// exists, plain http otherwise, with the host port appended when the edge
// is published on a non-default one.
func routeURL(route client.RouteStatus, ports edgePorts) string {
	scheme, port, defaultPort := "http", ports.HTTP, 80
	if route.Certificate != nil {
		scheme, port, defaultPort = "https", ports.HTTPS, 443
	}
	url := scheme + "://" + route.Domain
	if port != 0 {
		url += localdev.HostPortSuffix(port, defaultPort)
	}
	if route.Path != "" && route.Path != "/" {
		url += route.Path
	}
	return url
}

// devProcessNote names the host ports a dev process listens on; a single
// port drops its name, several keep them so each maps to its SKALI_PORT_*
// variable.
func devProcessNote(style *clirender.Style, ports map[string]int) string {
	if len(ports) == 0 {
		return ""
	}
	entries := make([]string, 0, len(ports))
	for _, name := range utils.SortedKeys(ports) {
		entry := "localhost:" + strconv.Itoa(ports[name])
		if len(ports) > 1 {
			entry = name + "=" + entry
		}
		entries = append(entries, entry)
	}
	return style.Dim("-> dev process on " + strings.Join(entries, ", "))
}
