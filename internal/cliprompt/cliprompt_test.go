package cliprompt

import (
	"bufio"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func reader(input string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(input))
}

func TestLine(t *testing.T) {
	t.Parallel()
	answer, err := Line(reader("  hello  \n"), "? ")
	require.NoError(t, err)
	require.Equal(t, "hello", answer)

	// A final line without a newline still answers.
	answer, err = Line(reader("last"), "? ")
	require.NoError(t, err)
	require.Equal(t, "last", answer)

	_, err = Line(reader(""), "? ")
	require.Error(t, err)
}

func TestLineDefault(t *testing.T) {
	t.Parallel()
	answer, err := LineDefault(reader("\n"), "? ", "production")
	require.NoError(t, err)
	require.Equal(t, "production", answer)

	answer, err = LineDefault(reader("e2e\n"), "? ", "production")
	require.NoError(t, err)
	require.Equal(t, "e2e", answer)
}

func TestConfirm(t *testing.T) {
	t.Parallel()
	require.True(t, Confirm(reader("y\n"), "? "))
	require.True(t, Confirm(reader("YES\n"), "? "))
	require.False(t, Confirm(reader("\n"), "? "))
	require.False(t, Confirm(reader("n\n"), "? "))
	require.False(t, Confirm(reader(""), "? "))

	require.True(t, ConfirmDefaultYes(reader("\n"), "? "))
	require.True(t, ConfirmDefaultYes(reader("y\n"), "? "))
	require.False(t, ConfirmDefaultYes(reader("n\n"), "? "))
}

func TestConfirmTyped(t *testing.T) {
	t.Parallel()
	require.True(t, ConfirmTyped(reader("production\n"), "? ", "production"))
	require.False(t, ConfirmTyped(reader("prod\n"), "? ", "production"))
	require.False(t, ConfirmTyped(reader("\n"), "? ", "production"))
}

func TestSelect(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	choice, err := Select(reader("2\n"), &out, ": ", []string{"server", "agent"}, 0)
	require.NoError(t, err)
	require.Equal(t, 1, choice)
	require.Contains(t, out.String(), "[1] server")
	require.Contains(t, out.String(), "[2] agent")

	// Empty answer picks the fallback.
	choice, err = Select(reader("\n"), &out, ": ", []string{"server", "agent"}, 0)
	require.NoError(t, err)
	require.Equal(t, 0, choice)

	// Out-of-range answers re-prompt until valid.
	choice, err = Select(reader("7\nx\n1\n"), &out, ": ", []string{"server", "agent"}, -1)
	require.NoError(t, err)
	require.Equal(t, 0, choice)
}
