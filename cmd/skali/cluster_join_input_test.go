package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/stretchr/testify/require"
)

func TestJoinTokenSourcesAndNormalization(t *testing.T) {
	token, _, err := clusterstate.NewToken("invitation", "sha256:"+strings.Repeat("1", 64))
	require.NoError(t, err)
	file := t.TempDir() + "/token"
	wrapped := token[:25] + "\r\n  " + token[25:] + "\n"
	require.NoError(t, os.WriteFile(file, []byte(wrapped), 0600))
	for _, test := range []struct {
		name, flag, file, env, stdin string
		tokenSet, fileSet            bool
		wantErr                      bool
	}{
		{name: "flag", flag: token, tokenSet: true},
		{name: "env", env: wrapped},
		{name: "file", file: file, fileSet: true},
		{name: "stdin", file: "-", fileSet: true, stdin: wrapped},
		{name: "flag overrides environment", flag: token, tokenSet: true, env: "bad"},
		{name: "file overrides environment", file: file, fileSet: true, env: "bad"},
		{name: "conflicting sources", flag: token, file: file, tokenSet: true, fileSet: true, wantErr: true},
		{name: "empty flag", tokenSet: true, env: token, wantErr: true},
		{name: "malformed", flag: "skali.invalid", tokenSet: true, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := joinTokenInput(context.Background(), test.flag, test.file, test.tokenSet, test.fileSet, test.env, bytes.NewBufferString(test.stdin))
			if test.wantErr {
				require.Error(t, err)
				require.NotContains(t, err.Error(), token)
				return
			}
			require.NoError(t, err)
			require.Equal(t, token, got)
		})
	}
	got, err := joinTokenInput(context.Background(), "", "", false, false, "", strings.NewReader(""))
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestJoinCapabilityDefaultsAndOverrides(t *testing.T) {
	ctx := context.Background()
	caps, err := joinCapabilities(ctx, nil, []string{"database"}, false)
	require.NoError(t, err)
	require.Equal(t, []string{"database"}, caps)
	caps, err = joinCapabilities(ctx, []string{"application"}, []string{"application", "edge"}, false)
	require.NoError(t, err)
	require.Equal(t, []string{"application"}, caps)
	_, err = joinCapabilities(ctx, nil, nil, false)
	require.ErrorContains(t, err, "--capabilities")
}
