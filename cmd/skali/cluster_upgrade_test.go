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
	printUpgradePlan(&out, drifted, layout.RoleServer, "skalid:dev", true, false)
	require.Equal(t, ""+
		"  k3s     v1.33.2+k3s1 -> v1.33.3+k3s1\n"+
		"  bundle  1.0.0 -> 1.1.0\n"+
		"  skalid  skalid:dev (imported from tar)\n\n", out.String())

	tarForced := installer.UpgradePlan{
		K3sFrom: "v1.33.3+k3s1", K3sTo: "v1.33.3+k3s1",
		BundleFrom: "1.1.0", BundleTo: "1.1.0", ImageForced: true,
	}
	out.Reset()
	printUpgradePlan(&out, tarForced, layout.RoleServer, "skalid:dev", true, false)
	require.Equal(t, ""+
		"  k3s     v1.33.3+k3s1 (current)\n"+
		"  bundle  1.1.0 (reconverge for the new skalid image)\n"+
		"  skalid  skalid:dev (imported from tar)\n\n", out.String())

	k3sOnly := installer.UpgradePlan{
		K3sFrom: "v1.33.2+k3s1", K3sTo: "v1.33.3+k3s1", K3sDrifted: true,
		BundleFrom: "1.1.0", BundleTo: "1.1.0",
	}
	out.Reset()
	printUpgradePlan(&out, k3sOnly, layout.RoleServer, "ghcr.io/hinkolas/skalid:1.1.0", false, false)
	require.Equal(t, ""+
		"  k3s     v1.33.2+k3s1 -> v1.33.3+k3s1\n"+
		"  bundle  1.1.0 (reconverge to republish the record)\n"+
		"  skalid  ghcr.io/hinkolas/skalid:1.1.0\n\n", out.String())

	agent := installer.UpgradePlan{
		K3sFrom: "v1.33.2+k3s1", K3sTo: "v1.33.3+k3s1", K3sDrifted: true,
	}
	out.Reset()
	printUpgradePlan(&out, agent, layout.RoleAgent, "", false, false)
	require.Equal(t, ""+
		"  k3s     v1.33.2+k3s1 -> v1.33.3+k3s1\n\n"+
		"This node is an agent: upgrade the server first, then run upgrade on each node.\n\n",
		out.String())

	// A pre-token-auth host: the credential line names the restart only
	// when no k3s upgrade will restart the service anyway.
	out.Reset()
	printUpgradePlan(&out, drifted, layout.RoleServer, "skalid:dev", true, true)
	require.Contains(t, out.String(), "  pull    node registry credential missing, minted during this upgrade\n")
	require.NotContains(t, out.String(), "k3s restarts")

	out.Reset()
	printUpgradePlan(&out, tarForced, layout.RoleServer, "skalid:dev", true, true)
	require.Contains(t, out.String(),
		"  pull    node registry credential missing, minted during this upgrade; "+
			"k3s restarts to load it (containers keep running)\n")
}
