package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
)

// newClusterResetPasswordCmd is the lockout recovery path: there is no
// email-based reset by design, so a forgotten admin password is reset by
// whoever holds root on a server node.
func newClusterResetPasswordCommand() *cobra.Command {
	var (
		email            string
		disableTwoFactor bool
		passwordStdin    bool
	)
	command := &cobra.Command{
		Use:   "reset-password",
		Short: "Reset a user's password from a server node",
		Long: "Reset a user's password without knowing the old one. This is the recovery\n" +
			"path when nobody can sign in anymore: it runs on a server node with root\n" +
			"access and needs no working login. Every session of the user is revoked;\n" +
			"--disable-2fa also removes a lost authenticator.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			out := os.Stdout
			banner(out)
			reader := bufio.NewReader(os.Stdin)
			return runResetPassword(command.Context(), out, reader, email, passwordStdin, disableTwoFactor)
		},
	}
	command.Flags().StringVar(&email, "email", "", "login email of the user (prompted when omitted)")
	command.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the new password from stdin instead of prompting")
	command.Flags().BoolVar(&disableTwoFactor, "disable-2fa", false, "also remove the user's two-factor enrollment")
	return command
}

func runResetPassword(ctx context.Context, out *os.File, reader *bufio.Reader,
	email string, passwordStdin, disableTwoFactor bool) error {
	if _, err := darwinPrelude(ctx, out, vmPolicyMaintain, ""); err != nil {
		return err
	}
	detected, err := installer.Detect(ctx, runner())
	if err != nil {
		return err
	}
	switch detected.State {
	case installer.StateServer:
	case installer.StateAgent:
		return errors.New("password resets run on a server node")
	case installer.StateUnmanaged:
		return unmanagedError()
	case installer.StateDamaged:
		return fmt.Errorf("this installation is damaged: %s; run skali cluster diagnose",
			strings.Join(detected.Problems, "; "))
	default:
		return fmt.Errorf("no skali installation on this host (state %s); run skali cluster install first",
			detected.State)
	}

	// Every input is settled before anything touches the cluster.
	if email == "" {
		if !cliprompt.Interactive() {
			return errors.New("non-interactive run requires --email")
		}
		email, err = cliprompt.Line(reader, "  email           : ")
		if err != nil {
			return err
		}
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return errors.New("an email is required")
	}
	var password string
	if passwordStdin {
		data, err := reader.ReadString('\n')
		if err != nil && data == "" {
			return fmt.Errorf("read password from stdin: %w", err)
		}
		password = strings.TrimRight(data, "\r\n")
	} else {
		if !cliprompt.Interactive() {
			return errors.New("non-interactive run requires --password-stdin")
		}
		password, err = promptNewPassword(reader)
		if err != nil {
			return err
		}
	}
	if password == "" {
		return errors.New("the password must not be empty")
	}

	client, err := installer.KubeClient(ctx, runner())
	if err != nil {
		return err
	}
	image, err := bundle.DeployedSkalidImage(ctx, client)
	if err != nil {
		return err
	}

	fmt.Fprintln(out)
	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	if err := bundle.ResetUserPassword(ctx, client, image, email, password, disableTwoFactor, progress); err != nil {
		progress.Abort()
		return err
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "password reset for %s; every session of the user was signed out\n", email)
	if disableTwoFactor {
		fmt.Fprintln(out, "two-factor authentication was removed; re-enroll it from the account page")
	}
	return nil
}

// promptNewPassword mirrors the init prompt: no echo, entered twice.
func promptNewPassword(reader *bufio.Reader) (string, error) {
	for {
		password, err := cliprompt.Secret(reader, "  new password    : ")
		if err != nil {
			return "", err
		}
		if password == "" {
			fmt.Fprintln(os.Stderr, "the password must not be empty")
			continue
		}
		repeat, err := cliprompt.Secret(reader, "  repeat password : ")
		if err != nil {
			return "", err
		}
		if password == repeat {
			return password, nil
		}
		fmt.Fprintln(os.Stderr, "the passwords do not match; try again")
	}
}
