// Package cliprompt provides the interactive controls shared by the Skali
// command-line programs. Real terminals get cursor-aware Huh controls;
// pipes, tests, dumb terminals, and accessibility mode get deterministic
// line-oriented prompts without ANSI control sequences.
package cliprompt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/Hinkolas/skali/internal/clirender"
	"golang.org/x/term"
)

// ErrAborted is returned when the user interrupts an active prompt.
var ErrAborted = errors.New("prompt aborted")

const optionDescriptionSeparator = "\x1f"

// Option separates a choice's stored value from its user-facing copy.
type Option struct {
	Label       string
	Description string
	Value       string
}

// TextOptions configures a single-line input.
type TextOptions struct {
	Title       string
	Description string
	Default     string
	Placeholder string
	CharLimit   int
	Validate    func(string) error
}

// SecretOptions configures a masked single-line input.
type SecretOptions struct {
	Title       string
	Description string
	Validate    func(string) error
}

// SelectOptions configures a single-choice prompt.
type SelectOptions struct {
	Title        string
	Description  string
	Options      []Option
	DefaultValue string
}

// MultiSelectOptions configures a multiple-choice prompt.
type MultiSelectOptions struct {
	Title         string
	Description   string
	Options       []Option
	DefaultValues []string
	Limit         int
	Validate      func([]string) error
}

// ConfirmOptions configures a yes/no prompt.
type ConfirmOptions struct {
	Title       string
	Description string
	Default     bool
}

// Session owns prompt input, output, mode, and the buffered reader used by
// plain prompts. A Session can be injected in tests and shared by a complete
// conversation so piped input is never lost between questions.
type Session struct {
	in          io.Reader
	out         io.Writer
	reader      *bufio.Reader
	interactive bool
	accessible  bool
	noColor     bool
}

// New creates a prompt session and derives its behavior from the supplied
// streams and environment. SKALI_ACCESSIBLE forces line-oriented prompts on a
// terminal; TERM=dumb does the same. NO_COLOR keeps the interactive controls
// but removes color.
func New(in io.Reader, out io.Writer) *Session {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stderr
	}
	reader, ok := in.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(in)
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	return &Session{
		in:          in,
		out:         out,
		reader:      reader,
		interactive: clirender.IsTerminal(in) && clirender.IsTerminal(out),
		accessible:  os.Getenv("SKALI_ACCESSIBLE") == "1" || os.Getenv("TERM") == "dumb",
		noColor:     noColor,
	}
}

// NewPlain creates a deterministic line-oriented session regardless of the
// supplied streams. It is intended for tests and explicitly piped workflows.
func NewPlain(in io.Reader, out io.Writer) *Session {
	session := New(in, out)
	session.interactive = false
	session.accessible = false
	session.noColor = true
	return session
}

// Interactive reports whether both session streams are terminals. Accessible
// and dumb-terminal modes may still choose the plain renderer on those TTYs.
func (s *Session) Interactive() bool { return s.interactive }

// Interactive reports whether stdin and stdout are terminals; command policy
// uses it to decide whether asking a question is allowed at all.
func Interactive() bool {
	return clirender.IsTerminal(os.Stdin) && clirender.IsTerminal(os.Stdout)
}

func (s *Session) terminalUI() bool {
	return s.interactive && !s.accessible
}

// Text asks for editable single-line text.
func (s *Session) Text(ctx context.Context, options TextOptions) (string, error) {
	if !s.terminalUI() {
		return s.plainText(options)
	}
	var value string
	field := huh.NewInput().
		Title(promptTitle(options.Title, "", s.noColor)).
		Description(options.Description).
		Placeholder(textInputPlaceholder(options)).
		Prompt(" ").
		Value(&value)
	if options.CharLimit > 0 {
		field.CharLimit(options.CharLimit)
	}
	normalize := func(raw string) string {
		normalized := strings.TrimSpace(raw)
		if normalized == "" {
			return options.Default
		}
		return normalized
	}
	if options.Validate != nil {
		field.Validate(func(raw string) error {
			return options.Validate(normalize(raw))
		})
	}
	if err := s.run(ctx, field); err != nil {
		return "", err
	}
	value = normalize(value)
	s.settle(options.Title, value, false)
	return value, nil
}

// Secret asks for editable masked text. The settled transcript never contains
// the entered value.
func (s *Session) Secret(ctx context.Context, options SecretOptions) (string, error) {
	if !s.terminalUI() {
		return s.plainSecret(options)
	}
	var value string
	field := huh.NewInput().
		Title(promptTitle(options.Title, "type, use arrows to edit, enter to confirm", s.noColor)).
		Description(options.Description).
		EchoMode(huh.EchoModePassword).
		Prompt(" ").
		Value(&value)
	if options.Validate != nil {
		field.Validate(options.Validate)
	}
	if err := s.run(ctx, field); err != nil {
		return "", err
	}
	s.settle(options.Title, "entered", true)
	return value, nil
}

// Select asks for exactly one option.
func (s *Session) Select(ctx context.Context, options SelectOptions) (string, error) {
	if len(options.Options) == 0 {
		return "", errors.New("select prompt has no options")
	}
	if !s.terminalUI() {
		return s.plainSelect(options)
	}
	value := options.DefaultValue
	choices := make([]huh.Option[string], 0, len(options.Options))
	for _, option := range options.Options {
		choices = append(choices, huh.NewOption(selectOptionText(option), option.Value))
	}
	field := huh.NewSelect[string]().
		Title(promptTitle(options.Title, "use arrow keys, enter to select", s.noColor)).
		Description(options.Description).
		Options(choices...).
		Value(&value).
		Height(listHeight(len(choices), options.Description))
	if err := s.run(ctx, field); err != nil {
		return "", err
	}
	s.settle(options.Title, optionLabel(options.Options, value), false)
	return value, nil
}

// MultiSelect asks for zero or more options.
func (s *Session) MultiSelect(ctx context.Context, options MultiSelectOptions) ([]string, error) {
	if len(options.Options) == 0 {
		return nil, errors.New("multi-select prompt has no options")
	}
	if !s.terminalUI() {
		return s.plainMultiSelect(options)
	}
	values := append([]string(nil), options.DefaultValues...)
	choices := make([]huh.Option[string], 0, len(options.Options))
	for _, option := range options.Options {
		choice := huh.NewOption(optionText(option), option.Value)
		if slices.Contains(values, option.Value) {
			choice = choice.Selected(true)
		}
		choices = append(choices, choice)
	}
	field := huh.NewMultiSelect[string]().
		Title(promptTitle(options.Title, "use arrows and space, enter to confirm", s.noColor)).
		Description(options.Description).
		Options(choices...).
		Value(&values).
		Height(listHeight(len(choices), options.Description)).
		Filterable(len(choices) > 7)
	if options.Limit > 0 {
		field.Limit(options.Limit)
	}
	if options.Validate != nil {
		field.Validate(options.Validate)
	}
	if err := s.run(ctx, field); err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(values))
	for _, value := range values {
		labels = append(labels, optionLabel(options.Options, value))
	}
	s.settle(options.Title, strings.Join(labels, ", "), false)
	return values, nil
}

// Confirm asks a yes/no question with an explicit safe default.
func (s *Session) Confirm(ctx context.Context, options ConfirmOptions) (bool, error) {
	if !s.terminalUI() {
		return s.plainConfirm(options)
	}
	value := options.Default
	field := newConfirmField(options, &value, s.noColor)
	if err := s.run(ctx, field); err != nil {
		return false, err
	}
	answer := "No"
	if value {
		answer = "Yes"
	}
	s.settle(options.Title, answer, false)
	return value, nil
}

func newConfirmField(options ConfirmOptions, value *bool, noColor bool) *huh.Confirm {
	title := promptTitle(options.Title, "use arrows or y/n, enter to confirm", noColor)
	description := ""
	if options.Description == "" {
		title += "\n"
	} else {
		description = "\n" + options.Description + "\n"
	}
	return huh.NewConfirm().
		Title(title).
		Description(description).
		Affirmative(" Yes /").
		Negative("No").
		Value(value).
		Inline(true).
		WithButtonAlignment(lipgloss.Left)
}

// ConfirmTyped asks the user to type an exact value. It is used for
// destructive operations and renders only "confirmed" after success.
func (s *Session) ConfirmTyped(ctx context.Context, title, description, expected string) (bool, error) {
	validate := func(value string) error {
		if value != expected {
			return fmt.Errorf("type %q exactly to continue", expected)
		}
		return nil
	}
	if !s.terminalUI() {
		value, err := s.plainText(TextOptions{
			Title:       title,
			Description: description,
			Validate:    validate,
		})
		return value == expected, err
	}
	var value string
	field := newTypedConfirmField(title, expected, &value, validate, s.noColor)
	err := s.run(ctx, field)
	if err != nil {
		return false, err
	}
	s.settle(title, "confirmed", false)
	return value == expected, nil
}

func newTypedConfirmField(
	title, expected string,
	value *string,
	validate func(string) error,
	noColor bool,
) *huh.Input {
	return huh.NewInput().
		Title(promptTitle(title, fmt.Sprintf("type %q to confirm", expected), noColor)).
		Prompt(" ").
		Value(value).
		Validate(validate)
}

func (s *Session) run(ctx context.Context, field huh.Field) error {
	form := huh.NewForm(huh.NewGroup(field)).
		WithInput(s.in).
		WithOutput(s.out).
		WithTheme(skaliTheme(s.noColor)).
		WithAccessible(false).
		WithShowHelp(false).
		WithShowErrors(true)
	err := form.RunWithContext(ctx)
	if errors.Is(err, huh.ErrUserAborted) || errors.Is(err, context.Canceled) {
		return ErrAborted
	}
	return err
}

func (s *Session) settle(title, value string, secret bool) {
	if !s.terminalUI() {
		return
	}
	accent, answer := settledStyles(s.noColor)
	fmt.Fprintf(s.out, "%s  %s\n", accent.Render("◆"), title)
	if secret {
		fmt.Fprintf(s.out, "%s  %s\n", accent.Render("└"), answer.Render("entered"))
		return
	}
	fmt.Fprintf(s.out, "%s  %s\n", accent.Render("└"), answer.Render(value))
}

func promptTitle(title, hint string, noColor bool) string {
	palette := newPromptPalette(noColor)
	rendered := palette.accent.Render("◆") + "  " + palette.selected.Render(title)
	if hint != "" {
		rendered += "  " + palette.hint.Render("("+hint+")")
	}
	return rendered
}

func textInputPlaceholder(options TextOptions) string {
	if options.Default != "" {
		return " (hit Enter to use '" + options.Default + "')"
	}
	if options.Placeholder != "" {
		return " " + options.Placeholder
	}
	return ""
}

func optionText(option Option) string {
	if option.Description == "" {
		return option.Label
	}
	return option.Label + " (" + option.Description + ")"
}

func selectOptionText(option Option) string {
	value := "○ " + option.Label
	if option.Description != "" {
		value += optionDescriptionSeparator + option.Description
	}
	return value
}

func splitSelectOption(value string) (label, description string) {
	label, description, _ = strings.Cut(value, optionDescriptionSeparator)
	return label, description
}

func optionLabel(options []Option, value string) string {
	for _, option := range options {
		if option.Value == value {
			return option.Label
		}
	}
	return value
}

func listHeight(count int, description string) int {
	// Huh's Height includes the title and description. Account for both so
	// the option viewport itself always has room for every choice.
	height := count + 1
	if description != "" {
		height++
	}
	return height
}

func skaliTheme(noColor bool) huh.Theme {
	return huh.ThemeFunc(func(isDark bool) *huh.Styles {
		theme := huh.ThemeBase(isDark)
		palette := newPromptPalette(noColor)

		// Keep one quiet row beneath the active form so prompts do not sit
		// directly against the terminal window edge.
		theme.Form.Base = lipgloss.NewStyle().PaddingBottom(1)
		// The milestone replaces the rail on the question row; subsequent
		// rows continue the connected prompt flow beneath it.
		theme.Focused.Base = activePromptBase(palette.accent)
		// Titles carry separately styled marker, question, and inline hint.
		theme.Focused.Title = lipgloss.NewStyle()
		theme.Focused.Description = palette.description.PaddingLeft(1)
		theme.Focused.ErrorIndicator = palette.danger.SetString("✗ ")
		theme.Focused.ErrorMessage = palette.danger
		theme.Focused.SelectSelector = lipgloss.NewStyle().SetString(" ")
		theme.Focused.Option = lipgloss.NewStyle()
		theme.Focused.MultiSelectSelector = palette.accent.SetString(" › ")
		theme.Focused.SelectedPrefix = palette.success.SetString("■ ")
		theme.Focused.UnselectedPrefix = palette.muted.SetString("□ ")
		theme.Focused.SelectedOption = lipgloss.NewStyle().
			Transform(func(value string) string {
				if !strings.HasPrefix(value, "○ ") {
					return palette.selected.Render(value)
				}
				label, description := splitSelectOption(strings.TrimPrefix(value, "○ "))
				rendered := palette.success.Render("●") + " " +
					palette.selected.Render(label)
				if description != "" {
					rendered += palette.muted.Render(" (" + description + ")")
				}
				return rendered
			})
		theme.Focused.UnselectedOption = lipgloss.NewStyle().
			Transform(func(value string) string {
				if !strings.HasPrefix(value, "○ ") {
					return palette.muted.Render(value)
				}
				label, _ := splitSelectOption(strings.TrimPrefix(value, "○ "))
				return palette.muted.Render("○ " + label)
			})
		theme.Focused.TextInput.Cursor = palette.success
		theme.Focused.TextInput.Prompt = palette.accent
		theme.Focused.TextInput.Placeholder = palette.muted
		theme.Focused.FocusedButton = lipgloss.NewStyle().
			Transform(func(value string) string {
				return renderConfirmChoice(value, true, palette)
			})
		theme.Focused.BlurredButton = lipgloss.NewStyle().
			Transform(func(value string) string {
				return renderConfirmChoice(value, false, palette)
			})
		theme.Blurred = theme.Focused
		theme.Blurred.Base = lipgloss.NewStyle().PaddingLeft(1)
		theme.Blurred.Title = lipgloss.NewStyle()
		theme.Blurred.Description = theme.Focused.Description
		theme.Group.Title = theme.Focused.Title
		theme.Group.Description = theme.Focused.Description
		return theme
	})
}

func activePromptBase(accent lipgloss.Style) lipgloss.Style {
	return lipgloss.NewStyle().Transform(func(value string) string {
		lines := strings.Split(value, "\n")
		for index := 1; index < len(lines); index++ {
			lines[index] = accent.Render("│") + " " + lines[index]
		}
		return strings.Join(lines, "\n")
	})
}

func renderConfirmChoice(value string, focused bool, palette promptPalette) string {
	indent := strings.HasPrefix(value, " ")
	value = strings.TrimPrefix(value, " ")
	hasSeparator := strings.HasSuffix(value, " /")
	label := strings.TrimSuffix(value, " /")

	marker := palette.muted.Render("○")
	renderedLabel := palette.muted.Render(label)
	if focused {
		marker = palette.success.Render("●")
		renderedLabel = palette.selected.Render(label)
	}

	rendered := marker + " " + renderedLabel
	if hasSeparator {
		rendered += palette.muted.Render(" / ")
	}
	if indent {
		rendered = " " + rendered
	}
	return rendered
}

type promptPalette struct {
	accent      lipgloss.Style
	success     lipgloss.Style
	selected    lipgloss.Style
	muted       lipgloss.Style
	hint        lipgloss.Style
	description lipgloss.Style
	danger      lipgloss.Style
}

func newPromptPalette(noColor bool) promptPalette {
	if noColor {
		return promptPalette{}
	}
	// Bright ANSI colors follow the user's terminal palette, retaining
	// contrast across light and dark themes without hard-coding a background.
	return promptPalette{
		accent:      lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
		success:     lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		selected:    lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true),
		muted:       lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		hint:        lipgloss.NewStyle().Foreground(lipgloss.Color("7")),
		description: lipgloss.NewStyle().Foreground(lipgloss.Color("7")),
		danger:      lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
	}
}

func settledStyles(noColor bool) (lipgloss.Style, lipgloss.Style) {
	if noColor {
		return lipgloss.NewStyle(), lipgloss.NewStyle()
	}
	palette := newPromptPalette(false)
	return palette.accent, palette.muted
}

func (s *Session) plainText(options TextOptions) (string, error) {
	if options.Description != "" {
		fmt.Fprintln(s.out, options.Description)
	}
	for {
		prompt := options.Title
		if options.Default != "" {
			prompt += " [" + options.Default + "]"
		}
		fmt.Fprint(s.out, prompt+": ")
		line, err := s.reader.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		value := strings.TrimSpace(line)
		if value == "" {
			value = options.Default
		}
		if options.CharLimit > 0 && len([]rune(value)) > options.CharLimit {
			fmt.Fprintf(s.out, "input cannot exceed %d characters\n", options.CharLimit)
			continue
		}
		if options.Validate != nil {
			if err := options.Validate(value); err != nil {
				fmt.Fprintln(s.out, err)
				continue
			}
		}
		return value, nil
	}
}

func (s *Session) plainSecret(options SecretOptions) (string, error) {
	// A terminal in accessibility mode can still suppress echo.
	if file, ok := s.in.(interface{ Fd() uintptr }); ok && clirender.IsTerminal(s.in) {
		for {
			fmt.Fprint(s.out, options.Title+": ")
			value, err := term.ReadPassword(int(file.Fd()))
			fmt.Fprintln(s.out)
			if err != nil {
				return "", err
			}
			answer := string(value)
			if options.Validate != nil {
				if err := options.Validate(answer); err != nil {
					fmt.Fprintln(s.out, err)
					continue
				}
			}
			return answer, nil
		}
	}
	return s.plainText(TextOptions{
		Title:    options.Title,
		Validate: options.Validate,
	})
}

func (s *Session) plainSelect(options SelectOptions) (string, error) {
	defaultIndex := -1
	fmt.Fprintln(s.out, options.Title+":")
	if options.Description != "" {
		fmt.Fprintln(s.out, "  "+options.Description)
	}
	for index, option := range options.Options {
		fmt.Fprintf(s.out, "  %d) %s\n", index+1, optionText(option))
		if option.Value == options.DefaultValue {
			defaultIndex = index
		}
	}
	for {
		prompt := fmt.Sprintf("Select [1-%d]", len(options.Options))
		if defaultIndex >= 0 {
			prompt += fmt.Sprintf(" (%d)", defaultIndex+1)
		}
		fmt.Fprint(s.out, prompt+": ")
		line, err := s.reader.ReadString('\n')
		answer := strings.TrimSpace(line)
		if answer == "" && defaultIndex >= 0 {
			return options.Options[defaultIndex].Value, nil
		}
		choice, conversionErr := strconv.Atoi(answer)
		if conversionErr == nil && choice >= 1 && choice <= len(options.Options) {
			return options.Options[choice-1].Value, nil
		}
		if err != nil {
			return "", errors.New("no valid selection")
		}
		fmt.Fprintf(s.out, "please answer 1-%d\n", len(options.Options))
	}
}

func (s *Session) plainMultiSelect(options MultiSelectOptions) ([]string, error) {
	fmt.Fprintln(s.out, options.Title+":")
	if options.Description != "" {
		fmt.Fprintln(s.out, "  "+options.Description)
	}
	for index, option := range options.Options {
		marker := " "
		if slices.Contains(options.DefaultValues, option.Value) {
			marker = "x"
		}
		fmt.Fprintf(s.out, "  %d) [%s] %s\n", index+1, marker, optionText(option))
	}
	for {
		fmt.Fprint(s.out, "Select comma-separated numbers (empty keeps defaults): ")
		line, err := s.reader.ReadString('\n')
		if err != nil && line == "" {
			return nil, err
		}
		answer := strings.TrimSpace(line)
		values := append([]string(nil), options.DefaultValues...)
		if answer != "" {
			values = nil
			for part := range strings.SplitSeq(answer, ",") {
				index, conversionErr := strconv.Atoi(strings.TrimSpace(part))
				if conversionErr != nil || index < 1 || index > len(options.Options) {
					fmt.Fprintf(s.out, "please answer with numbers 1-%d\n", len(options.Options))
					values = nil
					break
				}
				value := options.Options[index-1].Value
				if !slices.Contains(values, value) {
					values = append(values, value)
				}
			}
			if values == nil {
				continue
			}
		}
		if options.Limit > 0 && len(values) > options.Limit {
			fmt.Fprintf(s.out, "select at most %d options\n", options.Limit)
			continue
		}
		if options.Validate != nil {
			if err := options.Validate(values); err != nil {
				fmt.Fprintln(s.out, err)
				continue
			}
		}
		return values, nil
	}
}

func (s *Session) plainConfirm(options ConfirmOptions) (bool, error) {
	suffix := "[y/N]"
	if options.Default {
		suffix = "[Y/n]"
	}
	if options.Description != "" {
		fmt.Fprintln(s.out, options.Description)
	}
	for {
		fmt.Fprintf(s.out, "%s %s ", options.Title, suffix)
		line, err := s.reader.ReadString('\n')
		if err != nil && line == "" {
			return false, err
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		switch answer {
		case "":
			return options.Default, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(s.out, "please answer yes or no")
		}
	}
}

// Legacy adapters keep small internal callers source-compatible while all
// rendering and parsing still flows through Session.

func legacySession(r *bufio.Reader, out io.Writer) *Session {
	if Interactive() {
		return New(os.Stdin, out)
	}
	return NewPlain(r, out)
}

func legacyTitle(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	return strings.TrimSpace(strings.TrimSuffix(trimmed, ":"))
}

// Line asks for a line using the shared prompt layer.
func Line(r *bufio.Reader, prompt string) (string, error) {
	return legacySession(r, os.Stderr).Text(context.Background(), TextOptions{Title: legacyTitle(prompt)})
}

// LineDefault asks for a line with a default.
func LineDefault(r *bufio.Reader, prompt, fallback string) (string, error) {
	title := legacyTitle(prompt)
	title = strings.TrimSpace(strings.TrimSuffix(title, "["+fallback+"]"))
	return legacySession(r, os.Stderr).Text(context.Background(), TextOptions{
		Title: title, Default: fallback,
	})
}

// Secret asks for masked text on a terminal and a plain line on a pipe.
func Secret(r *bufio.Reader, prompt string) (string, error) {
	return legacySession(r, os.Stderr).Secret(context.Background(), SecretOptions{Title: legacyTitle(prompt)})
}
