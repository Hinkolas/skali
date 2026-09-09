package cliprompt

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/huh/v2"
)

// VisibleToken supports editable, wrapped, multiline paste. Only the active
// field displays the value; settled transcripts and errors never echo it.
func (s *Session) VisibleToken(ctx context.Context, validate func(string) error) (string, error) {
	var value string
	if s.terminalUI() {
		field := huh.NewText().Title("Join token").
			Description("Paste the complete token. Enter adds a line; Ctrl+D submits; Ctrl+C cancels.").
			Lines(6).CharLimit(16384).Value(&value)
		if validate != nil {
			field.Validate(func(v string) error { return validate(normalizeTokenInput(v)) })
		}
		keys := huh.NewDefaultKeyMap()
		keys.Text.NewLine.SetKeys("enter", "ctrl+j")
		keys.Text.Next.SetKeys("ctrl+d")
		keys.Text.Submit.SetKeys("ctrl+d")
		keys.Text.Editor.SetEnabled(false)
		if err := s.run(ctx, field, keys); err != nil {
			return "", err
		}
	} else {
		fmt.Fprintln(s.out, "Join token (visible; paste all lines, then a blank line to submit):")
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			line, err := s.reader.ReadString('\n')
			if strings.TrimSpace(line) == "" && len(value) > 0 {
				break
			}
			value += line
			if len(value) > 16384 {
				return "", fmt.Errorf("join token exceeds 16384 characters")
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
		}
	}
	value = normalizeTokenInput(value)
	if validate != nil {
		if err := validate(value); err != nil {
			return "", err
		}
	}
	s.settle("Join token", "entered", true)
	return value, nil
}

func normalizeTokenInput(value string) string { return strings.Join(strings.Fields(value), "") }
