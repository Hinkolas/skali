package cliprompt

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVisibleTokenMultilinePlain(t *testing.T) {
	var out bytes.Buffer
	session := NewPlain(strings.NewReader("skali.first\r\n  second\r\n\r\nnext prompt\n"), &out)
	token, err := session.VisibleToken(context.Background(), func(value string) error {
		if value != "skali.firstsecond" {
			return errors.New("invalid token")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, "skali.firstsecond", token)
	line, err := session.reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "next prompt\n", line)
	require.Contains(t, out.String(), "blank line")
	require.NotContains(t, out.String(), "skali.firstsecond")
}

func TestVisibleTokenBracketedPasteAndEditing(t *testing.T) {
	session, _, ctx, cancel := terminal("\x1b[200~skali.first\r\nsecond\x1b[201~\x04")
	defer cancel()
	token, err := session.VisibleToken(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "skali.firstsecond", token)
}

func TestVisibleTokenEnterAddsLineAndCtrlDSubmits(t *testing.T) {
	session, _, ctx, cancel := terminal("first\rsecond\x04")
	defer cancel()
	token, err := session.VisibleToken(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "firstsecond", token)
}

func TestVisibleTokenCancelled(t *testing.T) {
	session, _, ctx, cancel := terminal("\x03")
	defer cancel()
	_, err := session.VisibleToken(ctx, nil)
	require.ErrorIs(t, err, ErrAborted)
}
