package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/localdev"
)

// reauthSession refreshes the sudo window of the current session for a
// gated call: the local platform reuses its recorded bootstrap password,
// every other remote asks for the second factor when one is enrolled, opens
// the browser to confirm when the terminal is interactive (the console's
// checkpoint takes a password manager), and asks for the account password
// otherwise. On a pipe the prompt reads one line from stdin (the same
// plain prompt skali remote add uses), so scripts can confirm without a
// terminal.
func reauthSession(ctx context.Context, out io.Writer, in *bufio.Reader, api *client.Client) error {
	info, err := api.CurrentSession(ctx)
	if err != nil {
		return fmt.Errorf("reauthenticate: %w", err)
	}
	// The local platform's own admin never types the generated password;
	// other accounts on it (test members) confirm like on any remote.
	if api.Master() == localdev.MasterURL() && info.User.Email == localdev.AdminEmail {
		return reauthLocal(ctx, api)
	}
	session := promptSession(out, in)
	if info.User.TwoFactorEnabled {
		code, err := session.Text(ctx, cliprompt.TextOptions{
			Title:       "Two-factor code",
			Description: "Recent authentication required; enter the six-digit code from your authenticator.",
			CharLimit:   6,
			Validate:    validateTOTPCode,
		})
		if err != nil {
			return reauthPromptError(err)
		}
		if err := api.ReauthenticateWithCode(ctx, code); err != nil {
			return fmt.Errorf("reauthenticate: %w", err)
		}
		return nil
	}
	if browserAuthAvailable(false) {
		err := deviceReauth(ctx, out, api)
		if !client.IsNotFound(err) {
			if err != nil {
				return fmt.Errorf("reauthenticate: %w", err)
			}
			return nil
		}
		// An older daemon without the device flow: confirm the password.
	}
	password, err := session.Secret(ctx, cliprompt.SecretOptions{
		Title:       "Confirm your password",
		Description: "Recent authentication required for this command.",
		Validate: func(value string) error {
			if value == "" {
				return errors.New("password is required")
			}
			return nil
		},
	})
	if err != nil {
		return reauthPromptError(err)
	}
	if err := api.Reauthenticate(ctx, password); err != nil {
		return fmt.Errorf("reauthenticate: %w", err)
	}
	return nil
}

// reauthPromptError explains a failed prompt read: on a closed pipe the
// caller forgot to supply the password.
func reauthPromptError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("recent authentication required; confirm your password on stdin or re-run in a terminal")
	}
	return err
}

func validateTOTPCode(value string) error {
	if len(value) != 6 {
		return errors.New("enter a six-digit code")
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return errors.New("enter a six-digit code")
		}
	}
	return nil
}

// withReauth runs a sudo-gated call and, when the session's recent
// authentication has lapsed, confirms identity once and runs it again. The
// reader is the command's stdin, shared with any earlier prompt so a piped
// answer sequence is consumed in order.
func withReauth(ctx context.Context, out io.Writer, in *bufio.Reader, api *client.Client, call func() error) error {
	err := call()
	if !isReauthRequired(err) {
		return err
	}
	if err := reauthSession(ctx, out, in, api); err != nil {
		return err
	}
	return call()
}
