package main

import (
	"bufio"
	"io"
	"os"

	"github.com/Hinkolas/skali/internal/cliprompt"
)

// promptSession preserves the command's injectable plain reader in tests and
// piped workflows, while real terminal conversations use stdin directly so
// Huh can enable raw-mode editing and key handling.
func promptSession(out io.Writer, in *bufio.Reader) *cliprompt.Session {
	if cliprompt.Interactive() {
		return cliprompt.New(os.Stdin, out)
	}
	return cliprompt.NewPlain(in, out)
}
