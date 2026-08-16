package clirender

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLink(t *testing.T) {
	t.Parallel()
	plain := &Style{}
	require.Equal(t, "https://example.com", plain.Link("https://example.com"))
	styled := &Style{Enabled: true}
	require.Equal(t, "\x1b]8;;https://example.com\x1b\\\x1b[36mhttps://example.com\x1b[0m\x1b]8;;\x1b\\",
		styled.Link("https://example.com"))
}
