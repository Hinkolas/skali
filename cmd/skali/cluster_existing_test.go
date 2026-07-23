package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateExistingMode(t *testing.T) {
	// Not parallel: it mutates package-level flag vars.
	defer func() { modeFlag, kubeconfigFlag, imageTarFlag = "", "", "" }()

	cases := []struct {
		name       string
		mode       string
		kubeconfig string
		imageTar   string
		want       string
	}{
		{"managed default", "", "", "", ""},
		{"managed rejects kubeconfig", "", "/kc", "", "--kubeconfig applies only to --mode existing-cluster"},
		{"existing needs kubeconfig", "existing-cluster", "", "", "requires --kubeconfig"},
		{"existing rejects image tar", "existing-cluster", "/kc", "/tar", "--image-tar applies only to managed"},
		{"existing ok", "existing-cluster", "/kc", "", ""},
		{"unknown mode", "cloud", "", "", `unknown --mode "cloud"`},
	}
	for _, tc := range cases {
		modeFlag, kubeconfigFlag, imageTarFlag = tc.mode, tc.kubeconfig, tc.imageTar
		err := validateExistingMode()
		if tc.want == "" {
			require.NoError(t, err, tc.name)
		} else {
			require.ErrorContains(t, err, tc.want, tc.name)
		}
	}
}

func TestExistingModeRefusal(t *testing.T) {
	t.Parallel()
	require.ErrorContains(t, existingModeRefusal("join"), "existing-cluster mode never manages nodes")
	require.ErrorContains(t, existingModeRefusal("join"), "join applies to skali-managed")
}
