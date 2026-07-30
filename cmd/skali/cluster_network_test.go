package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/installer"
)

// networkPromptFixture scripts a plain prompt conversation: one input line
// per question, captured output for transcript assertions.
func networkPromptFixture(t *testing.T, input string) (*cliprompt.Session, *os.File) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "prompt-*.txt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = out.Close() })
	return cliprompt.NewPlain(strings.NewReader(input), out), out
}

func promptOutput(t *testing.T, out *os.File) string {
	t.Helper()
	data, err := os.ReadFile(out.Name())
	require.NoError(t, err)
	return string(data)
}

func TestPromptDetectedNodeNetworkKeepsDetectedDefaults(t *testing.T) {
	addresses := []installer.HostAddress{
		{IP: "10.0.1.2", Interface: "enp7s0", Private: true},
		{IP: "203.0.113.7", Interface: "eth0", DefaultRoute: true},
	}
	// Empty answers accept the defaults: the sole private address for the
	// cluster, the detected public address for the internet.
	session, out := networkPromptFixture(t, "\n\n")

	network, err := promptDetectedNodeNetwork(context.Background(), out, session, addresses)
	require.NoError(t, err)
	require.Equal(t, "10.0.1.2", network.ClusterIP)
	require.Equal(t, []string{"203.0.113.7"}, network.PublicIPs)
	require.Contains(t, promptOutput(t, out), "--coordinator-bind cluster,public")
}

func TestPromptDetectedNodeNetworkTypedPublicOnSingleHomedHost(t *testing.T) {
	addresses := []installer.HostAddress{
		{IP: "192.168.10.150", Interface: "eth0", Private: true, DefaultRoute: true},
	}
	// The single detected address settles the cluster silently; option 2 of
	// the internet question is "another address", which asks for typed ones.
	session, out := networkPromptFixture(t, "2\n203.0.113.7, 198.51.100.4\n")

	network, err := promptDetectedNodeNetwork(context.Background(), out, session, addresses)
	require.NoError(t, err)
	require.Equal(t, "192.168.10.150", network.ClusterIP)
	require.Equal(t, []string{"203.0.113.7", "198.51.100.4"}, network.PublicIPs)
	require.Contains(t, promptOutput(t, out), "node address: 192.168.10.150")
}

func TestPromptDetectedNodeNetworkTypedClusterAddress(t *testing.T) {
	addresses := []installer.HostAddress{
		{IP: "10.0.1.2", Interface: "enp7s0", Private: true},
		{IP: "10.0.2.2", Interface: "enp8s0", Private: true, DefaultRoute: true},
	}
	// Option 3 of the cluster question is "another address"; the first typed
	// answer is rejected as invalid and the prompt asks again.
	session, out := networkPromptFixture(t, "3\n300.1.1.1\n10.0.3.3\n\n")

	network, err := promptDetectedNodeNetwork(context.Background(), out, session, addresses)
	require.NoError(t, err)
	require.Equal(t, "10.0.3.3", network.ClusterIP)
	require.Empty(t, network.PublicIPs)
	require.Contains(t, promptOutput(t, out), `"300.1.1.1" is not a valid IP address`)
}

func TestPromptDetectedNodeNetworkCombinesSelectedAndTypedPublics(t *testing.T) {
	addresses := []installer.HostAddress{
		{IP: "10.0.1.2", Interface: "enp7s0", Private: true},
		{IP: "203.0.113.7", Interface: "eth0", DefaultRoute: true},
	}
	// Cluster keeps its default; the internet question selects the detected
	// public address and "another address" (option 3), then types one more.
	session, out := networkPromptFixture(t, "\n2,3\n198.51.100.4\n")

	network, err := promptDetectedNodeNetwork(context.Background(), out, session, addresses)
	require.NoError(t, err)
	require.Equal(t, "10.0.1.2", network.ClusterIP)
	require.Equal(t, []string{"203.0.113.7", "198.51.100.4"}, network.PublicIPs)
}

func TestPromptTypedPublicIPsEmptyMeansNone(t *testing.T) {
	session, _ := networkPromptFixture(t, "\n")

	public, err := promptTypedPublicIPs(context.Background(), session)
	require.NoError(t, err)
	require.Empty(t, public)
}

func TestSplitIPList(t *testing.T) {
	require.Equal(t, []string{"203.0.113.7", "198.51.100.4"},
		splitIPList(" 203.0.113.7 ,, 198.51.100.4 "))
	require.Empty(t, splitIPList("  "))
	require.Error(t, validateIPList("203.0.113.7, not-an-ip"))
	require.NoError(t, validateIPList("203.0.113.7"))
	require.Error(t, validateRequiredIP(" "))
	require.Error(t, validateRequiredIP("nope"))
	require.NoError(t, validateRequiredIP("10.0.0.1"))
}
