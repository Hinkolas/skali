package clirender

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Style paints terminal output with ANSI colors and symbol glyphs. A
// disabled (or nil) style passes text through unchanged, so non-TTY output
// stays plain ASCII and greppable.
type Style struct {
	Enabled bool
}

// StyleFor enables styling when out is a terminal and the environment does
// not opt out (NO_COLOR, TERM=dumb).
func StyleFor(out io.Writer) *Style {
	enabled := IsTerminal(out) &&
		os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	return &Style{Enabled: enabled}
}

// IsTerminal reports whether the stream is backed by a terminal; any
// value without a file descriptor is not one.
func IsTerminal(stream any) bool {
	file, ok := stream.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(file.Fd()))
}

func (s *Style) on() bool { return s != nil && s.Enabled }

func (s *Style) wrap(code, text string) string {
	if !s.on() || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s *Style) Green(text string) string  { return s.wrap("32", text) }
func (s *Style) Red(text string) string    { return s.wrap("31", text) }
func (s *Style) Yellow(text string) string { return s.wrap("33", text) }
func (s *Style) Cyan(text string) string   { return s.wrap("36", text) }
func (s *Style) BrightGreen(text string) string {
	return s.wrap("92", text)
}
func (s *Style) BrightCyan(text string) string {
	return s.wrap("96", text)
}
func (s *Style) BrightYellow(text string) string {
	return s.wrap("93", text)
}
func (s *Style) Muted(text string) string   { return s.wrap("90", text) }
func (s *Style) Dim(text string) string     { return s.wrap("2", text) }
func (s *Style) Bold(text string) string    { return s.wrap("1", text) }
func (s *Style) BoldRed(text string) string { return s.wrap("1;31", text) }

// Link renders a URL as a cyan OSC 8 hyperlink so terminals that support
// it open the target on click; the URL text itself stays visible, so
// terminals without OSC 8 still auto-detect it. Plain output is the bare
// URL.
func (s *Style) Link(url string) string {
	if !s.on() || url == "" {
		return url
	}
	return "\x1b]8;;" + url + "\x1b\\" + s.Cyan(url) + "\x1b]8;;\x1b\\"
}

// spinnerFrames animate running work; every frame is one terminal cell.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (s *Style) spinner(frame int) string {
	return spinnerFrames[frame%len(spinnerFrames)]
}

// Glyph is the single-cell styled status symbol; frame animates the
// running and waiting spinners.
func (s *Style) Glyph(status string, frame int) string {
	switch status {
	case "succeeded":
		return s.Green("✓")
	case "failed":
		return s.Red("✗")
	case "running":
		return s.Cyan(s.spinner(frame))
	case "waiting":
		return s.Yellow(s.spinner(frame))
	case "pending":
		return s.Dim("○")
	case "skipped":
		return s.Dim("»")
	case "cancelled":
		return s.Dim("⊘")
	}
	return s.Dim("•")
}

// Check is a green check prefix for result lines; it disappears entirely
// without styling so plain output keeps its unadorned wording.
func (s *Style) Check() string {
	if !s.on() {
		return ""
	}
	return s.Green("✓") + " "
}

// Cross is the failing counterpart of Check.
func (s *Style) Cross() string {
	if !s.on() {
		return ""
	}
	return s.Red("✗") + " "
}

// TerminalWidth reports the width of out when it is a terminal; the
// fallback keeps truncation sane for pipes that claimed a style anyway.
func TerminalWidth(out io.Writer) int {
	if file, ok := out.(interface{ Fd() uintptr }); ok {
		if width, _, err := term.GetSize(int(file.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return 100
}

// Truncate bounds text to width terminal cells, marking the cut.
func Truncate(text string, width int) string {
	if width <= 1 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	return string(runes[:width-1]) + "…"
}

func spaces(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.Repeat(" ", count)
}
