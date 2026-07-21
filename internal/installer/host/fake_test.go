package host

import (
	"bytes"
	"context"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFakeFilesystem(t *testing.T) {
	t.Parallel()
	fake := &Fake{}
	ctx := context.Background()

	_, err := fake.ReadFile(ctx, "/var/lib/skali/installation.yaml")
	require.ErrorIs(t, err, fs.ErrNotExist)

	require.NoError(t, fake.MkdirAll(ctx, "/var/lib/skali", 0o750))
	require.NoError(t, fake.WriteFile(ctx, "/var/lib/skali/installation.yaml", []byte("version: \"1\"\n"), 0o600))

	data, err := fake.ReadFile(ctx, "/var/lib/skali/installation.yaml")
	require.NoError(t, err)
	require.Equal(t, "version: \"1\"\n", string(data))

	info, err := fake.Stat(ctx, "/var/lib/skali/installation.yaml")
	require.NoError(t, err)
	require.True(t, info.Exists)
	require.Equal(t, fs.FileMode(0o600), info.Mode)

	// Implicit directory existence through a child.
	fake2 := &Fake{FS: map[string][]byte{"/etc/rancher/k3s/config.yaml": []byte("x")}}
	info, err = fake2.Stat(ctx, "/etc/rancher/k3s")
	require.NoError(t, err)
	require.True(t, info.Exists)
	require.True(t, info.Mode.IsDir())

	// Recursive remove clears the subtree and is idempotent.
	require.NoError(t, fake.Remove(ctx, "/var/lib/skali"))
	info, err = fake.Stat(ctx, "/var/lib/skali/installation.yaml")
	require.NoError(t, err)
	require.False(t, info.Exists)
	require.NoError(t, fake.Remove(ctx, "/var/lib/skali"))

	require.Equal(t, []string{
		"mkdir /var/lib/skali",
		"write /var/lib/skali/installation.yaml",
		"remove /var/lib/skali",
		"remove /var/lib/skali",
	}, fake.Writes)
}

func TestFakeCommands(t *testing.T) {
	t.Parallel()
	fake := &Fake{Handlers: map[string]func(Command) (Result, error){
		"systemctl": func(cmd Command) (Result, error) {
			if len(cmd.Args) == 2 && cmd.Args[0] == "is-active" {
				return Result{ExitCode: 3, Stdout: "inactive\n"}, nil
			}
			return Result{}, nil
		},
	}}
	ctx := context.Background()

	result, err := fake.Run(ctx, Command{Name: "systemctl", Args: []string{"is-active", "k3s"}})
	require.NoError(t, err)
	require.Equal(t, 3, result.ExitCode)
	require.Equal(t, "inactive\n", result.Stdout)

	// A streaming writer receives the output instead of the result.
	var stream bytes.Buffer
	result, err = fake.Run(ctx, Command{Name: "systemctl", Args: []string{"is-active", "k3s"}, Stdout: &stream})
	require.NoError(t, err)
	require.Empty(t, result.Stdout)
	require.Equal(t, "inactive\n", stream.String())

	// Undeclared commands fail loudly.
	_, err = fake.Run(ctx, Command{Name: "k3s"})
	require.ErrorContains(t, err, "no handler for command")

	require.Len(t, fake.Commands, 3)
	require.Empty(t, fake.Writes)
}
