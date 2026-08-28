// Package browser opens URLs in the user's default browser. Callers treat
// failure as a hint to print the URL instead; nothing here is fatal.
package browser

import (
	"errors"
	"os/exec"
	"runtime"
)

// openCommand builds the platform's opener; tests replace it.
var openCommand = func(url string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url), nil
	case "linux":
		return exec.Command("xdg-open", url), nil
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url), nil
	}
	return nil, errors.New("browser: no opener for " + runtime.GOOS)
}

// Open launches the URL in the default browser without waiting for it.
// Output of the opener is discarded so it never garbles the terminal.
func Open(url string) error {
	cmd, err := openCommand(url)
	if err != nil {
		return err
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the opener in the background; its exit status is not news.
	go func() { _ = cmd.Wait() }()
	return nil
}
