package main

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClusterCommandsPreserveRemoteUpgradeOnMacOS(t *testing.T) {
	t.Cleanup(func() { imageTarFlag = "" })
	for _, tc := range []struct {
		name       string
		args       []string
		flags      []string
		remoteOnly bool
	}{
		{name: "install", args: []string{"install"}},
		{name: "remote update", args: []string{"upgrade"}, remoteOnly: true},
		{name: "remote observation", args: []string{"upgrade"}, flags: []string{"--observe-operation", "operation-id"}, remoteOnly: true},
		{name: "controller recovery", args: []string{"upgrade"}, flags: []string{"--recover"}},
		{name: "source install", args: []string{"upgrade"}, flags: []string{"--image-tar", "image.tar"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := newClusterCommand()
			command, _, err := cluster.Find(tc.args)
			require.NoError(t, err)
			require.NoError(t, command.ParseFlags(tc.flags))
			err = cluster.PersistentPreRunE(command, nil)
			if tc.remoteOnly || runtime.GOOS == "linux" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "cluster hosts require Linux")
			}
		})
	}
}
