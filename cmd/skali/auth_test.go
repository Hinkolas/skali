package main

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cliconfig"
)

// runCapturingStdout executes fn with os.Stdout redirected into the
// returned string; the auth commands print with fmt.Println directly.
func runCapturingStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	require.NoError(t, err)
	previous := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = previous }()
	runErr := fn()
	require.NoError(t, write.Close())
	output, err := io.ReadAll(read)
	require.NoError(t, err)
	return string(output), runErr
}

func TestAuthTokenCommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	require.NoError(t, cliconfig.Save(&cliconfig.Config{
		CurrentContext: "dev",
		Contexts: map[string]*cliconfig.Context{
			"dev": {Master: "http://skali.localhost:7070", Token: "session-token-value"},
		},
	}))

	output, err := runCapturingStdout(t, newAuthTokenCmd().Execute)
	require.NoError(t, err)
	require.Equal(t, "session-token-value\n", output)
}

func TestAuthTokenCommandNotLoggedIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	require.NoError(t, cliconfig.Save(&cliconfig.Config{
		CurrentContext: "dev",
		Contexts: map[string]*cliconfig.Context{
			"dev": {Master: "http://skali.localhost:7070"},
		},
	}))

	cmd := newAuthTokenCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	require.ErrorContains(t, err, "not logged in")
	require.ErrorContains(t, err, "skali auth login")
}
