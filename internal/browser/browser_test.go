package browser

import (
	"errors"
	"os/exec"
	"testing"
)

func TestOpenUsesOpener(t *testing.T) {
	var got string
	openCommand = func(url string) (*exec.Cmd, error) {
		got = url
		return exec.Command("true"), nil
	}
	t.Cleanup(func() { openCommand = nil })
	if err := Open("https://example.test/auth/device?code=BCDF-2345"); err != nil {
		t.Fatal(err)
	}
	if got != "https://example.test/auth/device?code=BCDF-2345" {
		t.Fatalf("opened %q", got)
	}

	openCommand = func(string) (*exec.Cmd, error) { return nil, errors.New("no opener") }
	if err := Open("https://example.test"); err == nil {
		t.Fatal("expected an error when no opener exists")
	}
}
