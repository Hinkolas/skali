// Package cliprompt holds the hand-rolled interactive primitives the CLIs
// share: line and secret prompts, y/N and type-the-name-back confirms, and
// a numbered selection. Prompts write to stderr so piped stdout stays
// clean; answers come from one shared reader so buffered input is never
// lost between prompts. There is deliberately no TUI dependency.
package cliprompt

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// Interactive reports whether both stdin and stdout are terminals; only
// then may a command prompt.
func Interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// Line prints a prompt to stderr and reads one trimmed line.
func Line(r *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// LineDefault prompts like Line but substitutes fallback for an empty
// answer.
func LineDefault(r *bufio.Reader, prompt, fallback string) (string, error) {
	answer, err := Line(r, prompt)
	if err != nil {
		return "", err
	}
	if answer == "" {
		return fallback, nil
	}
	return answer, nil
}

// Secret reads without echo on a terminal, and falls back to a plain line
// read when stdin is piped (scripts, CI).
func Secret(r *bufio.Reader, prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return Line(r, prompt)
	}
	fmt.Fprint(os.Stderr, prompt)
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(secret), nil
}

// Confirm asks a y/N question; only y and yes answer true.
func Confirm(r *bufio.Reader, prompt string) bool {
	answer, err := Line(r, prompt)
	if err != nil {
		return false
	}
	answer = strings.ToLower(answer)
	return answer == "y" || answer == "yes"
}

// ConfirmDefaultYes asks a Y/n question; empty, y, and yes answer true.
func ConfirmDefaultYes(r *bufio.Reader, prompt string) bool {
	answer, err := Line(r, prompt)
	if err != nil {
		return false
	}
	answer = strings.ToLower(answer)
	return answer == "" || answer == "y" || answer == "yes"
}

// ConfirmTyped requires the expected name typed back exactly; the guard
// for destructive operations.
func ConfirmTyped(r *bufio.Reader, prompt, expected string) bool {
	answer, err := Line(r, prompt)
	if err != nil {
		return false
	}
	return answer == expected
}

// Select prompts with numbered options and returns the chosen index; an
// empty answer picks fallback (pass -1 to require an answer).
func Select(r *bufio.Reader, out io.Writer, prompt string, options []string, fallback int) (int, error) {
	for index, option := range options {
		fmt.Fprintf(out, "  [%d] %s\n", index+1, option)
	}
	for {
		answer, err := Line(r, prompt)
		if err != nil {
			return 0, err
		}
		if answer == "" && fallback >= 0 {
			return fallback, nil
		}
		choice, err := strconv.Atoi(answer)
		if err == nil && choice >= 1 && choice <= len(options) {
			return choice - 1, nil
		}
		fmt.Fprintf(os.Stderr, "please answer 1-%d\n", len(options))
	}
}
