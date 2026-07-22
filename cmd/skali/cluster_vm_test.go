package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveVMName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		flagChanged bool
		flagValue   string
		configName  string
		want        string
		wantErr     string
	}{
		{name: "default", flagValue: "skali", want: "skali"},
		{name: "config wins over default", flagValue: "skali", configName: "skali-e2e-darwin", want: "skali-e2e-darwin"},
		{name: "changed flag wins", flagChanged: true, flagValue: "skali-two", want: "skali-two"},
		{name: "flag and config agree", flagChanged: true, flagValue: "same", configName: "same", want: "same"},
		{name: "flag and config disagree", flagChanged: true, flagValue: "one", configName: "two",
			wantErr: `the --vm flag ("one") and the config vm.name ("two") disagree`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveVMName(testCase.flagChanged, testCase.flagValue, testCase.configName)
			if testCase.wantErr != "" {
				require.ErrorContains(t, err, testCase.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
		})
	}
}
