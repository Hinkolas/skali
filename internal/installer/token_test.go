package installer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

func tokenServerRecord() *Record {
	return &Record{
		Version:        RecordVersion,
		InstallationID: "0f0f0f0f",
		Cluster:        "e2e",
		Node:           NodeRecord{Name: "cp-1", Role: layout.RoleServer},
	}
}

func TestCreateJoinToken(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{
		FS: map[string][]byte{
			K3sRegistriesPath: []byte(k3sRegistriesYAML("pull-secret-value")),
		},
		Handlers: map[string]func(host.Command) (host.Result, error){
			"k3s": func(cmd host.Command) (host.Result, error) {
				switch cmd.Args[0] {
				case "token":
					require.Equal(t, []string{"token", "create", "--ttl", JoinTokenTTL}, cmd.Args)
					return host.Result{Stdout: "K10abc::node:secret\n"}, nil
				case "kubectl":
					return host.Result{Stdout: `{"status":{"addresses":[
					{"type":"InternalIP","address":"10.0.0.5"},
					{"type":"Hostname","address":"cp-1"}]}}`}, nil
				}
				return host.Result{ExitCode: 1}, nil
			},
		},
	}
	token, err := CreateJoinToken(context.Background(), fake, tokenServerRecord())
	require.NoError(t, err)
	require.Equal(t, "e2e", token.Cluster)
	require.Equal(t, "https://10.0.0.5:6443", token.ServerURL)

	// The printed token is the composite form carrying both credentials.
	k3sToken, pullSecret, err := decodeJoinToken(token.Token)
	require.NoError(t, err)
	require.Equal(t, "K10abc::node:secret", k3sToken)
	require.Equal(t, "pull-secret-value", pullSecret)
}

func TestCreateJoinTokenHostnameFallback(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"k3s": func(cmd host.Command) (host.Result, error) {
			if cmd.Args[0] == "token" {
				return host.Result{Stdout: "level=info msg=starting\nK10abc::node:secret\n"}, nil
			}
			return host.Result{ExitCode: 1, Stderr: "connection refused"}, nil
		},
	}}
	token, err := CreateJoinToken(context.Background(), fake, tokenServerRecord())
	require.NoError(t, err)
	require.Equal(t, "https://cp-1:6443", token.ServerURL,
		"a failed address lookup falls back to the node name")
	k3sToken, pullSecret, err := decodeJoinToken(token.Token)
	require.NoError(t, err)
	require.Equal(t, "K10abc::node:secret", k3sToken,
		"log preamble ahead of the token is tolerated")
	require.Empty(t, pullSecret, "no registries.yaml means no pull credential")
}

func TestCreateJoinTokenFailure(t *testing.T) {
	t.Parallel()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"k3s": func(host.Command) (host.Result, error) {
			return host.Result{ExitCode: 1, Stderr: "token: server not running\n"}, nil
		},
	}}
	_, err := CreateJoinToken(context.Background(), fake, tokenServerRecord())
	require.ErrorContains(t, err, "exit code 1")
	require.ErrorContains(t, err, "server not running")
}

func TestCreateJoinTokenRefusesAgents(t *testing.T) {
	t.Parallel()
	record := tokenServerRecord()
	record.Node.Role = layout.RoleAgent
	fake := &host.Fake{}
	_, err := CreateJoinToken(context.Background(), fake, record)
	require.ErrorContains(t, err, "join tokens are created on a server node")
	require.Empty(t, fake.Commands)
}
