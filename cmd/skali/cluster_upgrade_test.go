package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
)

func inputReader(input string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(input))
}

func TestSeedInitInputsFullRecord(t *testing.T) {
	record := &installer.Record{
		Endpoints: &installer.Endpoints{API: "skali.example.com", Registry: "registry.example.com"},
		TLS:       &installer.TLSConfig{IssuerEmail: "ops@example.com"},
	}
	opts := installer.InitOptions{}
	// An empty reader proves nothing is prompted: any prompt would error
	// on EOF.
	require.NoError(t, seedInitInputs(inputReader(""), true, record, &opts))
	require.Equal(t, "skali.example.com", opts.Endpoints.API)
	require.Equal(t, "registry.example.com", opts.Endpoints.Registry)
	require.Equal(t, "ops@example.com", opts.TLS.IssuerEmail)
}

func TestSeedInitInputsMissingRegistryPrompts(t *testing.T) {
	// A record written before the registry domain existed: the prompt
	// offers the derivation from the api domain and empty input takes it.
	record := &installer.Record{
		Endpoints: &installer.Endpoints{API: "skali.example.com"},
		TLS:       &installer.TLSConfig{IssuerEmail: "ops@example.com"},
	}
	opts := installer.InitOptions{}
	require.NoError(t, seedInitInputs(inputReader("\n"), true, record, &opts))
	require.Equal(t, "registry.example.com", opts.Endpoints.Registry)
}

func TestSeedInitInputsMissingRegistryNonInteractive(t *testing.T) {
	record := &installer.Record{
		Endpoints: &installer.Endpoints{API: "skali.example.com"},
		TLS:       &installer.TLSConfig{IssuerEmail: "ops@example.com"},
	}
	opts := installer.InitOptions{}
	err := seedInitInputs(inputReader(""), false, record, &opts)
	require.ErrorContains(t, err, "registry domain")
	require.ErrorContains(t, err, "interactively")
}

func TestSeedInitInputsNilEndpointsPromptsAll(t *testing.T) {
	// The oldest records carry no endpoints or tls blocks at all; every
	// field is prompted in order.
	record := &installer.Record{}
	opts := installer.InitOptions{}
	input := "skali.example.com\n\nops@example.com\n"
	require.NoError(t, seedInitInputs(inputReader(input), true, record, &opts))
	require.Equal(t, "skali.example.com", opts.Endpoints.API)
	require.Equal(t, "registry.example.com", opts.Endpoints.Registry)
	require.Equal(t, "ops@example.com", opts.TLS.IssuerEmail)
}

func TestPrintUpgradePlanVariants(t *testing.T) {
	drifted := installer.UpgradePlan{
		K3sFrom: "v1.33.2+k3s1", K3sTo: "v1.33.3+k3s1", K3sDrifted: true,
		BundleFrom: "1.0.0", BundleTo: "1.1.0", BundleDrifted: true,
	}
	var out bytes.Buffer
	printUpgradePlan(&out, drifted, layout.RoleServer, "", "skalid:dev", true, false)
	require.Equal(t, ""+
		"  k3s     v1.33.2+k3s1 -> v1.33.3+k3s1\n"+
		"  bundle  1.0.0 -> 1.1.0\n"+
		"  skalid  skalid:dev (imported from tar)\n\n", out.String())

	tarForced := installer.UpgradePlan{
		K3sFrom: "v1.33.3+k3s1", K3sTo: "v1.33.3+k3s1",
		BundleFrom: "1.1.0", BundleTo: "1.1.0", ImageForced: true,
	}
	out.Reset()
	printUpgradePlan(&out, tarForced, layout.RoleServer, "", "skalid:dev", true, false)
	require.Equal(t, ""+
		"  k3s     v1.33.3+k3s1 (current)\n"+
		"  bundle  1.1.0 (reconverge for the new skalid image)\n"+
		"  skalid  skalid:dev (imported from tar)\n\n", out.String())

	k3sOnly := installer.UpgradePlan{
		K3sFrom: "v1.33.2+k3s1", K3sTo: "v1.33.3+k3s1", K3sDrifted: true,
		BundleFrom: "1.1.0", BundleTo: "1.1.0",
	}
	out.Reset()
	printUpgradePlan(&out, k3sOnly, layout.RoleServer, "", "ghcr.io/hinkolas/skalid:1.1.0", false, false)
	require.Equal(t, ""+
		"  k3s     v1.33.2+k3s1 -> v1.33.3+k3s1\n"+
		"  bundle  1.1.0 (reconverge to republish the record)\n"+
		"  skalid  ghcr.io/hinkolas/skalid:1.1.0\n\n", out.String())

	agent := installer.UpgradePlan{
		K3sFrom: "v1.33.2+k3s1", K3sTo: "v1.33.3+k3s1", K3sDrifted: true,
	}
	out.Reset()
	printUpgradePlan(&out, agent, layout.RoleAgent, "", "", false, false)
	require.Equal(t, ""+
		"  k3s     v1.33.2+k3s1 -> v1.33.3+k3s1\n\n"+
		"This node is an agent: upgrade the servers first, one at a time, then each agent.\n\n",
		out.String())

	// A secondary server: its bundle is maintained by the init owner, so
	// the plan names that node and stops at k3s.
	out.Reset()
	printUpgradePlan(&out, k3sOnly, layout.RoleServer, "cp-1", "", false, false)
	require.Equal(t, ""+
		"  k3s     v1.33.2+k3s1 -> v1.33.3+k3s1\n"+
		"  bundle  maintained on cp-1\n\n", out.String())

	// A pre-token-auth host: the credential line names the restart only
	// when no k3s upgrade will restart the service anyway.
	out.Reset()
	printUpgradePlan(&out, drifted, layout.RoleServer, "", "skalid:dev", true, true)
	require.Contains(t, out.String(), "  pull    node registry credential missing, minted during this upgrade\n")
	require.NotContains(t, out.String(), "k3s restarts")

	out.Reset()
	printUpgradePlan(&out, tarForced, layout.RoleServer, "", "skalid:dev", true, true)
	require.Contains(t, out.String(),
		"  pull    node registry credential missing, minted during this upgrade; "+
			"k3s restarts to load it (containers keep running)\n")
}

func TestUpgradeSequenceOrdering(t *testing.T) {
	status := &installer.Status{
		Host: &installer.Host{Hostname: "cp-2"},
		Nodes: []installer.NodeStatus{
			{Name: "cp-1", Role: layout.RoleServer, K3sVersion: "v1.33.2+k3s1"},
			{Name: "cp-2", Role: layout.RoleServer, K3sVersion: "v1.33.2+k3s1"},
			{Name: "cp-3", Role: layout.RoleServer, K3sVersion: "v1.33.3+k3s1", Current: true},
			{Name: "db-1", Role: layout.RoleAgent, K3sVersion: "v1.33.2+k3s1"},
		},
	}
	steps := installer.UpgradeSequence(status)
	require.Len(t, steps, 3, "current nodes are omitted")
	require.Equal(t, "cp-2", steps[0].Name, "this host leads")
	require.True(t, steps[0].IsSelf)
	require.Equal(t, "cp-1", steps[1].Name, "remaining servers before agents")
	require.Equal(t, "db-1", steps[2].Name)

	var out bytes.Buffer
	printUpgradeSequence(&out, status)
	require.Contains(t, out.String(), "cluster upgrade order")
	require.Contains(t, out.String(), "(this host)")
	require.Contains(t, out.String(), "run there: sudo skali cluster upgrade")

	out.Reset()
	printRemainingUpgrades(&out, status)
	require.Equal(t, "next: run sudo skali cluster upgrade on cp-1, then db-1\n", out.String())
}
