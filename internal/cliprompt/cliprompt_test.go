package cliprompt

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
	xterm "golang.org/x/term"
)

func plain(input string) (*Session, *strings.Builder) {
	var out strings.Builder
	return NewPlain(strings.NewReader(input), &out), &out
}

func terminal(input string) (*Session, *strings.Builder, context.Context, context.CancelFunc) {
	var out strings.Builder
	session := NewPlain(strings.NewReader(input), &out)
	session.interactive = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	return session, &out, ctx, cancel
}

func TestPlainTextEditingContract(t *testing.T) {
	t.Parallel()
	session, out := plain("\ninvalid\nstaging\n")
	answer, err := session.Text(context.Background(), TextOptions{
		Title:   "Environment name",
		Default: "production",
		Validate: func(value string) error {
			if value == "invalid" {
				return errors.New("invalid environment")
			}
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, "production", answer)
	require.Equal(t, "Environment name [production]: ", out.String())

	session, out = plain("invalid\nstaging\n")
	answer, err = session.Text(context.Background(), TextOptions{
		Title: "Environment name",
		Validate: func(value string) error {
			if value == "invalid" {
				return errors.New("invalid environment")
			}
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, "staging", answer)
	require.Contains(t, out.String(), "invalid environment")
}

func TestPlainSecretDoesNotWriteValue(t *testing.T) {
	t.Parallel()
	session, out := plain("correct horse battery staple\n")
	answer, err := session.Secret(context.Background(), SecretOptions{Title: "Password"})
	require.NoError(t, err)
	require.Equal(t, "correct horse battery staple", answer)
	require.NotContains(t, out.String(), answer)
	require.Equal(t, "Password: ", out.String())
}

func TestPlainConfirmDefaults(t *testing.T) {
	t.Parallel()
	session, _ := plain("\n")
	answer, err := session.Confirm(context.Background(), ConfirmOptions{Title: "Create?"})
	require.NoError(t, err)
	require.False(t, answer)

	session, _ = plain("\n")
	answer, err = session.Confirm(context.Background(), ConfirmOptions{Title: "Initialize?", Default: true})
	require.NoError(t, err)
	require.True(t, answer)

	session, _ = plain("YES\n")
	answer, err = session.Confirm(context.Background(), ConfirmOptions{Title: "Create?"})
	require.NoError(t, err)
	require.True(t, answer)
}

func TestPlainTypedConfirmationReprompts(t *testing.T) {
	t.Parallel()
	session, out := plain("prod\nproduction\n")
	answer, err := session.ConfirmTyped(context.Background(),
		"Confirm destructive deployment", "Type the environment name.", "production")
	require.NoError(t, err)
	require.True(t, answer)
	require.Contains(t, out.String(), "Type the environment name.")
	require.Contains(t, out.String(), `type "production" exactly to continue`)
}

func TestPlainSelectMapsDisplayToValue(t *testing.T) {
	t.Parallel()
	session, out := plain("9\n2\n")
	answer, err := session.Select(context.Background(), SelectOptions{
		Title: "Role",
		Options: []Option{
			{Label: "Server", Description: "runs the control plane", Value: "server"},
			{Label: "Agent", Description: "runs workloads", Value: "agent"},
		},
		DefaultValue: "server",
	})
	require.NoError(t, err)
	require.Equal(t, "agent", answer)
	require.Contains(t, out.String(), "1) Server (runs the control plane)")
	require.Contains(t, out.String(), "please answer 1-2")
}

func TestPlainSelectUsesDefault(t *testing.T) {
	t.Parallel()
	session, _ := plain("\n")
	answer, err := session.Select(context.Background(), SelectOptions{
		Title: "Environment",
		Options: []Option{
			{Label: "Production", Value: "production"},
			{Label: "Staging", Value: "staging"},
		},
		DefaultValue: "staging",
	})
	require.NoError(t, err)
	require.Equal(t, "staging", answer)
}

func TestPlainMultiSelectDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	session, _ := plain("\n")
	answer, err := session.MultiSelect(context.Background(), MultiSelectOptions{
		Title: "Capabilities",
		Options: []Option{
			{Label: "Application", Value: "application"},
			{Label: "Database", Value: "database"},
			{Label: "Registry", Value: "registry"},
		},
		DefaultValues: []string{"application", "database"},
		Validate: func(values []string) error {
			if len(values) == 0 {
				return errors.New("select at least one capability")
			}
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"application", "database"}, answer)

	session, _ = plain("1,3\n")
	answer, err = session.MultiSelect(context.Background(), MultiSelectOptions{
		Title: "Capabilities",
		Options: []Option{
			{Label: "Application", Value: "application"},
			{Label: "Database", Value: "database"},
			{Label: "Registry", Value: "registry"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"application", "registry"}, answer)
}

func TestPlainOutputHasNoANSI(t *testing.T) {
	t.Parallel()
	session, out := plain("1\n")
	_, err := session.Select(context.Background(), SelectOptions{
		Title: "Role",
		Options: []Option{
			{Label: "Server", Value: "server"},
		},
	})
	require.NoError(t, err)
	require.NotContains(t, out.String(), "\x1b[")
}

// KNOWN FAILURE: this test panics inside charm.land/huh/v2 v2.0.3 with
// "interface conversion: tea.Model is nil, not compat.ViewModel". The
// terminal() helper feeds huh a fake reader/writer pair instead of a real
// PTY; when bubbletea fails to start on that input it returns a nil model,
// and huh's Form.run (form.go:708) type-asserts the model before checking
// the error, masking the real failure with a panic. v2.0.3 is the newest
// huh release, so there is no fix to pull yet. Options when this needs to
// go green: drive these cases through a real PTY like the tests at the
// bottom of this file, patch huh with a replace directive, or bump huh
// once a release checks the error first.
func TestTerminalTextEditingKeys(t *testing.T) {
	t.Run("home end and backspace", func(t *testing.T) {
		session, _, ctx, cancel := terminal("roduction\x1b[Hp\x1b[F!\x7f\r")
		defer cancel()

		answer, err := session.Text(ctx, TextOptions{Title: "Environment"})
		require.NoError(t, err)
		require.Equal(t, "production", answer)
	})

	t.Run("left and delete", func(t *testing.T) {
		session, _, ctx, cancel := terminal("prodXuction" + strings.Repeat("\x1b[D", 7) + "\x1b[3~\r")
		defer cancel()

		answer, err := session.Text(ctx, TextOptions{Title: "Environment"})
		require.NoError(t, err)
		require.Equal(t, "production", answer)
	})

	t.Run("default and whitespace normalization", func(t *testing.T) {
		session, _, ctx, cancel := terminal("\r")
		defer cancel()
		answer, err := session.Text(ctx, TextOptions{
			Title:   "Environment",
			Default: "production",
		})
		require.NoError(t, err)
		require.Equal(t, "production", answer)

		session, _, ctx, cancel = terminal("staging\r")
		defer cancel()
		answer, err = session.Text(ctx, TextOptions{
			Title:   "Environment",
			Default: "production",
		})
		require.NoError(t, err)
		require.Equal(t, "staging", answer)

		session, _, ctx, cancel = terminal("  staging  \r")
		defer cancel()
		answer, err = session.Text(ctx, TextOptions{Title: "Environment"})
		require.NoError(t, err)
		require.Equal(t, "staging", answer)
	})
}

func TestTerminalSelectKeys(t *testing.T) {
	session, _, ctx, cancel := terminal("\x1b[B\r")
	defer cancel()

	answer, err := session.Select(ctx, SelectOptions{
		Title: "Role",
		Options: []Option{
			{Label: "Server", Value: "server"},
			{Label: "Agent", Value: "agent"},
		},
		DefaultValue: "server",
	})
	require.NoError(t, err)
	require.Equal(t, "agent", answer)
}

func TestThemeUsesSingleChoiceStates(t *testing.T) {
	styles := skaliTheme(true).Theme(true)
	require.Equal(t, "○ Server", styles.Focused.UnselectedOption.Render("○ Server"))
	require.Equal(t, "● Agent", styles.Focused.SelectedOption.Render("○ Agent"))
	require.Equal(t, "Application", styles.Focused.SelectedOption.Render("Application"))
	require.Equal(t, "Database", styles.Focused.UnselectedOption.Render("Database"))
	encoded := selectOptionText(Option{
		Label:       "Create a new cluster",
		Description: "start the first server on this host",
	})
	require.Equal(t,
		"● Create a new cluster (start the first server on this host)",
		styles.Focused.SelectedOption.Render(encoded))
	require.Equal(t,
		"○ Create a new cluster",
		styles.Focused.UnselectedOption.Render(encoded))

	coloredStyles := skaliTheme(false).Theme(true)
	palette := newPromptPalette(false)
	require.Equal(t,
		palette.success.Render("●")+" "+
			palette.selected.Render("Create a new cluster")+
			palette.muted.Render(" (start the first server on this host)"),
		coloredStyles.Focused.SelectedOption.Render(encoded))
	require.Equal(t,
		palette.muted.Render("○ Create a new cluster"),
		coloredStyles.Focused.UnselectedOption.Render(encoded))

	require.Equal(t,
		"● Yes / ○ No",
		styles.Focused.FocusedButton.Render("Yes /")+
			styles.Focused.BlurredButton.Render("No"))
	require.Equal(t,
		"○ Yes / ● No",
		styles.Focused.BlurredButton.Render("Yes /")+
			styles.Focused.FocusedButton.Render("No"))
	require.Equal(t,
		" ○ Yes / ● No",
		styles.Focused.BlurredButton.Render(" Yes /")+
			styles.Focused.FocusedButton.Render("No"))
	require.Equal(t, " Type the value exactly.", styles.Focused.Description.Render("Type the value exactly."))
	require.Equal(t, 1, styles.Form.Base.GetPaddingBottom())

	require.False(t, styles.Focused.Base.GetBorderLeft())
	require.Zero(t, styles.Focused.Base.GetPaddingLeft())
	require.Equal(t, " ", styles.Focused.SelectSelector.String())
	require.Equal(t,
		"◆ Question\n│ ○ First \n│ ○ Second",
		styles.Focused.Base.Render("◆ Question\n○ First\n○ Second"))
	require.NotContains(t, styles.Focused.FocusedButton.Render("Yes"), "\x1b[")
	require.NotContains(t, styles.Focused.ErrorMessage.Render("invalid"), "\x1b[")
}

func TestPromptTitleKeepsHintInline(t *testing.T) {
	title := promptTitle("What would you like to do?", "use arrow keys, enter to select", true)
	require.Equal(t,
		"◆  What would you like to do?  (use arrow keys, enter to select)",
		title)
	require.NotContains(t, title, "\n")
	require.Equal(t,
		"◆  Where should the project be created?",
		promptTitle("Where should the project be created?", "", true))
}

func TestTextInputPlaceholderKeepsDefaultOutOfEditingBuffer(t *testing.T) {
	require.Equal(t,
		" (hit Enter to use './')",
		textInputPlaceholder(TextOptions{Default: "./"}))
	require.Equal(t,
		" project-name",
		textInputPlaceholder(TextOptions{Placeholder: "project-name"}))
	require.Empty(t, textInputPlaceholder(TextOptions{}))
}

func TestSettledPromptClosesMutedFlow(t *testing.T) {
	var out strings.Builder
	session := NewPlain(strings.NewReader(""), &out)
	session.interactive = true

	session.settle("How should this host join Skali?", "Create a new cluster", false)
	require.Equal(t,
		"◆  How should this host join Skali?\n└  Create a new cluster\n",
		out.String())

	_, answer := settledStyles(false)
	require.Equal(t,
		newPromptPalette(false).muted.Render("Create a new cluster"),
		answer.Render("Create a new cluster"))
}

func TestListHeightShowsEveryOption(t *testing.T) {
	require.Equal(t, 7, listHeight(6, ""))
	require.Equal(t, 8, listHeight(6, "Additional context"))
}

func TestTerminalMultiSelectKeys(t *testing.T) {
	session, _, ctx, cancel := terminal(" \x1b[B \r")
	defer cancel()

	answer, err := session.MultiSelect(ctx, MultiSelectOptions{
		Title: "Capabilities",
		Options: []Option{
			{Label: "Application", Value: "application"},
			{Label: "Database", Value: "database"},
			{Label: "Registry", Value: "registry"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"application", "database"}, answer)
}

func TestTerminalMultiSelectDefaults(t *testing.T) {
	session, _, ctx, cancel := terminal("\r")
	defer cancel()

	answer, err := session.MultiSelect(ctx, MultiSelectOptions{
		Title: "Capabilities",
		Options: []Option{
			{Label: "Application", Value: "application"},
			{Label: "Database", Value: "database"},
			{Label: "Registry", Value: "registry"},
		},
		DefaultValues: []string{"application", "database"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"application", "database"}, answer)
}

func TestTerminalSecretEditingIsRedacted(t *testing.T) {
	session, out, ctx, cancel := terminal("secrXet\x1b[D\x1b[D\x7f\r")
	defer cancel()

	answer, err := session.Secret(ctx, SecretOptions{Title: "Password"})
	require.NoError(t, err)
	require.Equal(t, "secret", answer)
	require.NotContains(t, out.String(), "secr")
	require.Contains(t, out.String(), "entered")
}

func TestTerminalCtrlCAborts(t *testing.T) {
	session, _, ctx, cancel := terminal("\x03")
	defer cancel()

	_, err := session.Text(ctx, TextOptions{Title: "Environment"})
	require.ErrorIs(t, err, ErrAborted)
}

func TestTerminalConfirmDefaultYes(t *testing.T) {
	session, _, ctx, cancel := terminal("\r")
	defer cancel()

	answer, err := session.Confirm(ctx, ConfirmOptions{
		Title:   "Initialize now?",
		Default: true,
	})
	require.NoError(t, err)
	require.True(t, answer)
}

func TestTerminalConfirmDefaultNo(t *testing.T) {
	session, _, ctx, cancel := terminal("\r")
	defer cancel()

	answer, err := session.Confirm(ctx, ConfirmOptions{Title: "Continue?"})
	require.NoError(t, err)
	require.False(t, answer)
}

func TestConfirmFieldUsesInlineRadioLayout(t *testing.T) {
	value := false
	field := newConfirmField(ConfirmOptions{Title: "Continue?"}, &value, true)
	field.WithTheme(skaliTheme(true))
	field.WithWidth(80)
	field.WithHeight(3)
	field.Focus()
	require.Contains(t, field.View(), "○ Yes / ● No")
	require.Contains(t, field.View(), "│  ○ Yes / ● No")

	value = true
	field = newConfirmField(ConfirmOptions{Title: "Continue?", Default: true}, &value, true)
	field.WithTheme(skaliTheme(true))
	field.WithWidth(80)
	field.WithHeight(3)
	field.Focus()
	require.Contains(t, field.View(), "● Yes / ○ No")
	require.Contains(t, field.View(), "│  ● Yes / ○ No")
}

func TestTypedConfirmationKeepsPromptCompactAndInputBelow(t *testing.T) {
	value := "skali-dev"
	field := newTypedConfirmField(
		"Confirm cluster removal",
		"skali-dev",
		&value,
		func(input string) error {
			if input != "skali-dev" {
				return errors.New("incorrect")
			}
			return nil
		},
		true,
	)
	field.WithTheme(skaliTheme(true))
	field.WithWidth(120)
	field.WithHeight(1)
	field.Focus()

	view := field.View()
	require.Contains(t, view,
		`◆  Confirm cluster removal  (type "skali-dev" to confirm)`)
	require.Contains(t, view, "\n│  skali-dev")
}

func TestPseudoTerminalEditingCancellationAndRestoration(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("SKALI_ACCESSIBLE", "")

	t.Run("editing restores state", func(t *testing.T) {
		master, slave, err := pty.Open()
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = master.Close()
			_ = slave.Close()
		})

		initial, err := xterm.GetState(int(slave.Fd()))
		require.NoError(t, err)
		session := New(slave, slave)
		require.True(t, session.Interactive())

		type result struct {
			value string
			err   error
		}
		resultC := make(chan result, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		go func() {
			value, promptErr := session.Text(ctx, TextOptions{Title: "Environment"})
			resultC <- result{value: value, err: promptErr}
		}()

		require.Eventually(t, func() bool {
			state, stateErr := xterm.GetState(int(slave.Fd()))
			return stateErr == nil && !reflect.DeepEqual(initial, state)
		}, time.Second, 5*time.Millisecond)
		_, err = master.Write([]byte("prodXuction" + strings.Repeat("\x1b[D", 7) + "\x1b[3~\r"))
		require.NoError(t, err)

		promptResult := <-resultC
		require.NoError(t, promptResult.err)
		require.Equal(t, "production", promptResult.value)
		restored, err := xterm.GetState(int(slave.Fd()))
		require.NoError(t, err)
		require.True(t, reflect.DeepEqual(initial, restored))
	})

	t.Run("control-c restores state", func(t *testing.T) {
		master, slave, err := pty.Open()
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = master.Close()
			_ = slave.Close()
		})

		initial, err := xterm.GetState(int(slave.Fd()))
		require.NoError(t, err)
		session := New(slave, slave)
		resultC := make(chan error, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		go func() {
			_, promptErr := session.Text(ctx, TextOptions{Title: "Environment"})
			resultC <- promptErr
		}()

		require.Eventually(t, func() bool {
			state, stateErr := xterm.GetState(int(slave.Fd()))
			return stateErr == nil && !reflect.DeepEqual(initial, state)
		}, time.Second, 5*time.Millisecond)
		_, err = master.Write([]byte{3})
		require.NoError(t, err)

		require.ErrorIs(t, <-resultC, ErrAborted)
		restored, err := xterm.GetState(int(slave.Fd()))
		require.NoError(t, err)
		require.True(t, reflect.DeepEqual(initial, restored))
	})
}

func TestPseudoTerminalEnvironmentModes(t *testing.T) {
	master, slave, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})

	t.Run("accessible override", func(t *testing.T) {
		t.Setenv("TERM", "xterm-256color")
		t.Setenv("SKALI_ACCESSIBLE", "1")
		session := New(slave, slave)
		require.True(t, session.Interactive())
		require.True(t, session.accessible)
		require.False(t, session.terminalUI())
	})

	t.Run("dumb terminal", func(t *testing.T) {
		t.Setenv("TERM", "dumb")
		t.Setenv("SKALI_ACCESSIBLE", "")
		session := New(slave, slave)
		require.True(t, session.Interactive())
		require.True(t, session.accessible)
		require.False(t, session.terminalUI())
	})

	t.Run("no color keeps keyboard interaction", func(t *testing.T) {
		t.Setenv("TERM", "xterm-256color")
		t.Setenv("SKALI_ACCESSIBLE", "")
		t.Setenv("NO_COLOR", "1")
		session := New(slave, slave)
		require.True(t, session.terminalUI())
		require.True(t, session.noColor)
	})
}
