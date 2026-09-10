package localdev

import (
	"errors"
	"testing"
)

func TestUnexportableHostImage(t *testing.T) {
	cases := map[string]bool{
		"docker image save x: exit status 1\nError response from daemon: no suitable export target found: " +
			"image with reference x was found but does not provide the specified platform (linux/arm64)": true,
		"ctr import x into node: exit status 1\nctr: content digest sha256:1ba6f4: not found": true,
		"ctr import x into node: exit status 1\nctr: unrecognized image format":               true,
		"docker image save x: exit status 1\nError response from daemon: No such image: x":    false,
		"the import reported success but the node does not hold the image":                    false,
	}
	for text, want := range cases {
		if got := unexportableHostImage(errors.New(text)); got != want {
			t.Errorf("unexportableHostImage(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestLacksPlatformFlag(t *testing.T) {
	if !lacksPlatformFlag(errors.New("docker image save x: exit status 125\nunknown flag: --platform")) {
		t.Error("old docker CLI not recognized")
	}
	if lacksPlatformFlag(errors.New("docker image save x: exit status 1\nError response from daemon: No such image: x")) {
		t.Error("unrelated failure taken for a missing flag")
	}
}
