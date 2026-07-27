package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/installer"
)

// promptNodeNetwork settles how this host is addressed. It asks only when
// the answer is not obvious: a single-homed host has nothing to decide, and
// a Mac-hosted node takes the managed VM's address. On a multi-homed cloud
// server the default-route address is the public one, so leaving this
// implicit is what puts cluster traffic, the enrollment listener, and the
// api certificate on the public interface.
func promptNodeNetwork(ctx context.Context, out *os.File, reader *bufio.Reader) (installer.NodeNetwork, error) {
	if runtime.GOOS == "darwin" {
		// The Lima path pins the VM's own address on the network the fleet
		// reaches it through; there is no second interface to choose.
		return installer.NodeNetwork{}, nil
	}
	addresses, err := installer.DetectHostAddresses(ctx, runner())
	if err != nil || len(addresses) == 0 {
		// Address detection is a convenience. Without it the install keeps
		// the k3s default rather than refusing.
		return installer.NodeNetwork{}, nil
	}
	if len(addresses) == 1 {
		fmt.Fprintf(out, "  node address: %s\n", addresses[0].Label())
		return installer.NodeNetwork{ClusterIP: addresses[0].IP}, nil
	}

	session := promptSession(out, reader)
	options := make([]cliprompt.Option, 0, len(addresses))
	for _, address := range addresses {
		options = append(options, cliprompt.Option{
			Label: address.IP, Description: addressDescription(address), Value: address.IP,
		})
	}
	clusterIP, err := session.Select(ctx, cliprompt.SelectOptions{
		Title:        "Which address do other cluster nodes reach this node through?",
		Description:  "Node traffic, enrollment, and the api certificate follow this choice.",
		Options:      options,
		DefaultValue: defaultClusterAddress(addresses),
	})
	if err != nil {
		return installer.NodeNetwork{}, err
	}
	network := installer.NodeNetwork{ClusterIP: clusterIP}

	public := publicAddresses(addresses)
	if len(public) == 0 {
		return network, nil
	}
	publicOptions := make([]cliprompt.Option, 0, len(addresses))
	for _, address := range addresses {
		publicOptions = append(publicOptions, cliprompt.Option{
			Label: address.IP, Description: addressDescription(address), Value: address.IP,
		})
	}
	selected, err := session.MultiSelect(ctx, cliprompt.MultiSelectOptions{
		Title:       "Which addresses are reachable from the internet?",
		Description: "They become this node's external address and enter the api certificate.",
		Options:     publicOptions, DefaultValues: public,
	})
	if err != nil {
		return installer.NodeNetwork{}, err
	}
	network.PublicIPs = selected
	if len(selected) > 0 && !slices.Contains(selected, clusterIP) {
		fmt.Fprintln(out, "  enrollment stays on the cluster address; pass "+
			"--coordinator-bind cluster,public to also serve it publicly.")
	}
	return network.Normalize(), nil
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

// defaultClusterAddress recommends the private address when there is
// exactly one, because a private network is what an operator attaches
// cluster nodes to; otherwise it recommends the address k3s would have
// picked by itself.
func defaultClusterAddress(addresses []installer.HostAddress) string {
	private := ""
	count := 0
	for _, address := range addresses {
		if address.Private {
			private = address.IP
			count++
		}
	}
	if count == 1 {
		return private
	}
	for _, address := range addresses {
		if address.DefaultRoute {
			return address.IP
		}
	}
	return addresses[0].IP
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
