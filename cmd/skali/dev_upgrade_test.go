package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/localdev"
)

// The plan names every version, migration, kept asset, and interruption
// before anything changes.
func TestDevUpgradePlan(t *testing.T) {
	plan := &localdev.UpgradePlan{
		Status:          localdev.ClusterStopped,
		FromVersion:     "v0.4.0",
		ToVersion:       "v0.5.0",
		FromSkalidImage: "ghcr.io/hinkolas/skalid:v0.4.0",
		ToSkalidImage:   "ghcr.io/hinkolas/skalid:v0.5.0",
		FromK3sImage:    "rancher/k3s:v1.35.9-k3s1",
		ToK3sImage:      "rancher/k3s:v1.36.3-k3s1",
		Resuming:        &localdev.UpgradeRecord{StartedAt: time.Date(2026, 10, 21, 9, 30, 0, 0, time.Local)},
		Components: []localdev.ComponentChange{
			{Repository: "ghcr.io/cloudnative-pg/cloudnative-pg", From: "1.29.2", To: "1.30.0"},
			{Repository: "chrislusf/seaweedfs", To: "4.40"},
		},
		SchemaInstalled: -1,
		SchemaTarget:    21,
		CA:              &localdev.CA{Fingerprint: "ab12"},
	}
	var out bytes.Buffer
	printDevUpgradePlan(&out, clirender.StyleFor(&out), plan)
	for _, line := range []string{
		"upgrade plan for local platform",
		"resumes       the upgrade started 2026-10-21 09:30, which did not finish",
		"release       v0.4.0 -> v0.5.0",
		"skalid        ghcr.io/hinkolas/skalid:v0.4.0 -> ghcr.io/hinkolas/skalid:v0.5.0",
		"k3s           rancher/k3s:v1.35.9-k3s1 -> rancher/k3s:v1.36.3-k3s1",
		"components    ghcr.io/cloudnative-pg/cloudnative-pg 1.29.2 -> 1.30.0",
		"              chrislusf/seaweedfs 4.40 (new)",
		"migrations    platform database up to schema 21, applied by skalid at start (current version unknown while stopped)",
		"development CA sha256:ab12",
		"interruption  the stopped platform starts",
		"the node container is replaced on the new k3s",
		"skalid restarts",
		"CNPG (every database restarts once), the object store",
		"afterwards    skali releases older than v0.5.0 can no longer run this platform",
	} {
		require.Contains(t, out.String(), line)
	}

	plan.Resuming = nil
	plan.SchemaInstalled = 18
	plan.Components = nil
	plan.FromK3sImage = plan.ToK3sImage
	out.Reset()
	printDevUpgradePlan(&out, clirender.StyleFor(&out), plan)
	require.Contains(t, out.String(), "migrations    platform database schema 18 -> 21 (3 forward-only, applied by skalid at start)")
	require.Contains(t, out.String(), "k3s           rancher/k3s:v1.36.3-k3s1 (unchanged)")
	require.NotContains(t, out.String(), "resumes")
	require.NotContains(t, out.String(), "components")
	require.NotContains(t, out.String(), "node container")
}

func TestUpgradeSkalidImage(t *testing.T) {
	withCLIVersion(t, "v0.5.0")
	image, err := upgradeSkalidImage("")
	require.NoError(t, err)
	require.Equal(t, "ghcr.io/hinkolas/skalid:v0.5.0", image)
	_, err = upgradeSkalidImage("skalid:dev")
	require.ErrorContains(t, err, "must be ghcr.io/hinkolas/skalid:v0.5.0")

	withCLIVersion(t, "v0.0.0-dev")
	_, err = upgradeSkalidImage("ghcr.io/hinkolas/skalid:v0.5.0")
	require.ErrorContains(t, err, "requires a development image")
	image, err = upgradeSkalidImage("skalid:mine")
	require.NoError(t, err)
	require.Equal(t, "skalid:mine", image)
}
