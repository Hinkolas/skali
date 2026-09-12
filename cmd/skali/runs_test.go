package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
)

func TestRunCancelConfirmsWithSummary(t *testing.T) {
	install := seedAccessScope(t)
	environmentID := "p1-e1"
	install.runs["run-1"] = client.Run{ID: "run-1", Kind: "deployment", Status: "running", EnvironmentID: &environmentID}
	install.runs["run-2"] = client.Run{ID: "run-2", Kind: "backup", Status: "succeeded"}

	// The summary names the run and where it runs; declining cancels nothing.
	out, err := runCommand(t, newRunCommand(), "n\n", "cancel", "run-1")
	require.ErrorContains(t, err, "aborted")
	require.Contains(t, out, "run          run-1 (deployment)")
	require.Contains(t, out, "status       running")
	require.Contains(t, out, "environment  production")
	require.Contains(t, out, "returns the target to the prior active revision")
	require.Contains(t, out, "Cancel this deployment run? [y/N]")
	require.Empty(t, install.posts)

	// Confirming cancels and reports the fallback; a finished run is refused
	// before any question.
	out, err = runCommand(t, newRunCommand(), "y\n", "cancel", "run-1")
	require.NoError(t, err)
	require.Contains(t, out, "run run-1 cancelled")
	require.Contains(t, out, "the target returned to the prior active revision")
	require.Equal(t, []string{"cancel:run-1"}, install.posts)
	_, err = runCommand(t, newRunCommand(), "", "cancel", "run-2")
	require.ErrorContains(t, err, "run run-2 is already succeeded")

	// Closed stdin needs --yes; --yes asks nothing.
	_, err = runCommand(t, newRunCommand(), "", "cancel", "run-1")
	require.ErrorContains(t, err, "non-interactive use requires --yes")
	out, err = runCommand(t, newRunCommand(), "", "cancel", "run-1", "--yes")
	require.NoError(t, err)
	require.NotContains(t, out, "[y/N]")
}
