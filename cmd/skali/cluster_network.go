package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/installer"
)

// manualAddressValue marks the option that switches a prompt from picking
// a detected address to typing one. No IP address can collide with it.
const manualAddressValue = "manual"

// promptNodeNetwork settles how this host is addressed. The cluster
// address is chosen among the detected ones: a single-homed host has
// nothing to decide, and a Mac-hosted node takes the managed VM's address.
// The public addresses are always asked, because the addresses a router
// maps onto this host (floating, NAT, port forwarding) are assigned to no
// interface and can only be typed.
func promptNodeNetwork(ctx context.Context, out *os.File, reader *bufio.Reader) (installer.NodeNetwork, error) {
	session := promptSession(out, reader)
	if runtime.GOOS == "darwin" {
		// The Lima path pins the VM's own address on the network the fleet
		// reaches it through; only the public declaration is left to ask.
		public, err := promptTypedPublicIPs(ctx, session)
		if err != nil {
			return installer.NodeNetwork{}, err
		}
		return installer.NodeNetwork{PublicIPs: public}.Normalize(), nil
	}
	addresses, err := installer.DetectHostAddresses(ctx, runner())
	if err != nil || len(addresses) == 0 {
		// Address detection is a convenience. Without it the install keeps
		// the k3s default rather than refusing.
		return installer.NodeNetwork{}, nil
	}
	return promptDetectedNodeNetwork(ctx, out, session, addresses)
}

// promptDetectedNodeNetwork drives the address conversation over one
// detected list. Both questions also take a typed address: the cluster
// question for an address on an interface the detection filters, the
// public question for addresses that are mapped onto this host without
// being assigned to it.
func promptDetectedNodeNetwork(ctx context.Context, out io.Writer, session *cliprompt.Session,
	addresses []installer.HostAddress, preferred ...string) (installer.NodeNetwork, error) {
	network := installer.NodeNetwork{}
	recommended := installer.DefaultClusterAddress(addresses)
	if len(preferred) > 0 && preferred[0] != "" {
		recommended = preferred[0]
	}
	if len(addresses) == 1 {
		fmt.Fprintf(out, "  node address: %s\n", addresses[0].Label())
		network.ClusterIP = addresses[0].IP
	} else {
		clusterIP, err := session.Select(ctx, cliprompt.SelectOptions{
			Title:       "Which address do other cluster nodes reach this node through?",
			Description: "Node traffic, enrollment, and the api certificate follow this choice.",
			Options: append(addressOptions(addresses), cliprompt.Option{
				Label:       "another address",
				Description: "type an address the detection missed",
				Value:       manualAddressValue,
			}),
			DefaultValue: recommended,
		})
		if err != nil {
			return installer.NodeNetwork{}, err
		}
		if clusterIP == manualAddressValue {
			clusterIP, err = session.Text(ctx, cliprompt.TextOptions{
				Title:       "Cluster address",
				Description: "Other nodes reach this node through it; it must be assigned to one of this host's interfaces.",
				Validate:    validateRequiredIP,
			})
			if err != nil {
				return installer.NodeNetwork{}, err
			}
		}
		network.ClusterIP = strings.TrimSpace(clusterIP)
	}

	selected, err := session.MultiSelect(ctx, cliprompt.MultiSelectOptions{
		Title:       "Which addresses are reachable from the internet?",
		Description: "They become this node's external address and enter the api certificate.",
		Options: append(addressOptions(addresses), cliprompt.Option{
			Label:       "another address",
			Description: "NAT or port-forwarded, not assigned to this host",
			Value:       manualAddressValue,
		}),
		DefaultValues: publicAddresses(addresses),
	})
	if err != nil {
		return installer.NodeNetwork{}, err
	}
	if slices.Contains(selected, manualAddressValue) {
		selected = slices.DeleteFunc(selected, func(value string) bool {
			return value == manualAddressValue
		})
		typed, typedErr := promptTypedPublicIPs(ctx, session)
		if typedErr != nil {
			return installer.NodeNetwork{}, typedErr
		}
		selected = append(selected, typed...)
	}
	network.PublicIPs = selected
	if len(selected) > 0 && !slices.Contains(selected, network.ClusterIP) {
		fmt.Fprintln(out, "  enrollment stays on the cluster address; pass "+
			"--coordinator-bind cluster,public to also serve it publicly.")
	}
	return network.Normalize(), nil
}

// promptTypedPublicIPs asks for the addresses a router maps onto this
// host. They are declared, never bound, so no detection can offer them.
func promptTypedPublicIPs(ctx context.Context, session *cliprompt.Session) ([]string, error) {
	value, err := session.Text(ctx, cliprompt.TextOptions{
		Title:       "Public addresses reachable from the internet",
		Description: "NAT-mapped or port-forwarded addresses that reach this node. Comma separated, empty for none.",
		Placeholder: "203.0.113.7, 203.0.113.8",
		Validate:    validateIPList,
	})
	if err != nil {
		return nil, err
	}
	return splitIPList(value), nil
}

func validateRequiredIP(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("an address is required")
	}
	if net.ParseIP(value) == nil {
		return fmt.Errorf("%q is not a valid IP address", value)
	}
	return nil
}

func validateIPList(value string) error {
	for _, address := range splitIPList(value) {
		if net.ParseIP(address) == nil {
			return fmt.Errorf("%q is not a valid IP address", address)
		}
	}
	return nil
}

func splitIPList(value string) []string {
	var result []string
	for part := range strings.SplitSeq(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func addressOptions(addresses []installer.HostAddress) []cliprompt.Option {
	options := make([]cliprompt.Option, 0, len(addresses)+1)
	for _, address := range addresses {
		options = append(options, cliprompt.Option{
			Label: address.IP, Description: addressDescription(address), Value: address.IP,
		})
	}
	return options
}

func addressDescription(address installer.HostAddress) string {
	scope := "public"
	if address.Private {
		scope = "private network"
	}
	if address.DefaultRoute {
		scope += ", default route"
	}
	return address.Interface + ", " + scope
}

func publicAddresses(addresses []installer.HostAddress) []string {
	var public []string
	for _, address := range addresses {
		if !address.Private {
			public = append(public, address.IP)
		}
	}
	return public
}

// describeNetwork renders a settled declaration for a confirmation line.
func describeNetwork(network installer.NodeNetwork) string {
	if network.ClusterIP == "" {
		return "automatic"
	}
	parts := []string{"cluster " + network.ClusterIP}
	if len(network.PublicIPs) > 0 {
		parts = append(parts, "public "+strings.Join(network.PublicIPs, ", "))
	}
	return strings.Join(parts, "; ")
}

func promptJoinNodeNetwork(ctx context.Context, out *os.File, reader *bufio.Reader, desired installer.NodeNetwork, endpoint string) (installer.NodeNetwork, error) {
	addresses, err := installer.DetectHostAddresses(ctx, runner())
	if err != nil || len(addresses) == 0 {
		if desired.ClusterIP == "" {
			desired.ClusterIP, err = promptSession(out, reader).Text(ctx, cliprompt.TextOptions{Title: "Cluster address", Description: "Enter this host's address that other cluster nodes can reach.", Validate: validateRequiredIP})
			if err != nil {
				return desired, err
			}
		}
		if desired.PublicIPs == nil {
			desired.PublicIPs, err = promptTypedPublicIPs(ctx, promptSession(out, reader))
		}
		return desired, err
	}
	recommended := desired.ClusterIP
	if recommended == "" {
		recommended = installer.CoordinatorRouteAddress(ctx, runner(), endpoint)
	}
	if desired.ClusterIP != "" {
		if desired.PublicIPs == nil {
			desired.PublicIPs, err = promptTypedPublicIPs(ctx, promptSession(out, reader))
		}
		return desired, err
	}
	selected, err := promptDetectedNodeNetwork(ctx, out, promptSession(out, reader), addresses, recommended)
	if err != nil {
		return desired, err
	}
	// Explicit declarations are authoritative. The caller only invokes this
	// helper for missing inputs, retaining SAN and bind configuration.
	if desired.ClusterIP == "" {
		desired.ClusterIP = selected.ClusterIP
	}
	if desired.PublicIPs == nil {
		desired.PublicIPs = selected.PublicIPs
	}
	return desired, nil
}
