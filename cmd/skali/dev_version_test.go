package main

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDevVersionReasonUsesFrozenSelection(t *testing.T) {
	withCLIVersion(t, "v0.4.0")
	old := invocationContext
	t.Cleanup(func() { invocationContext = old })
	invocationContext = &versionContext{Remote: "lab", Release: "v0.4.0", Source: "--remote", Mode: "offline"}
	text := devVersionReason()
	require.Contains(t, text, "skali-dev runs v0.4.0")
	require.Contains(t, text, "target: lab; source: --remote; mode: offline")
	require.NotContains(t, text, "changed")
	withCLIVersion(t, "v0.0.0-dev")
	require.Contains(t, devVersionReason(), "working tree")
}
