//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The first background session on a machine finds no state directory yet;
// skali dev expose -d writes nothing else there before its log.
func TestOpenSessionLogCreatesTheStateDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skali", "remote-dev", "expose.log")
	file, err := openSessionLog(path)
	require.NoError(t, err)
	_, err = file.WriteString("first run\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())

	file, err = openSessionLog(path)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Empty(t, content, "each start truncates the log")
}
