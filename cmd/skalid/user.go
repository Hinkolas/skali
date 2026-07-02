package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/config"
	"github.com/Hinkolas/skali/internal/store"
)

// runUser implements the operator CLI for app users. It loads only
// config.Base: user management needs DATABASE_URL and must not demand
// AUTH_SECRET or other serve-only settings.
func runUser(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: skalid user <create|list|delete> [flags]")
	}
	ctx := context.Background()

	cfg, err := config.Load[config.Base](ctx)
	if err != nil {
		return err
	}
	pool, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.NewStore(pool)

	switch args[0] {
	case "create":
		return userCreate(ctx, st, args[1:])
	case "list":
		return userList(ctx, st)
	case "delete":
		return userDelete(ctx, st, args[1:])
	default:
		return fmt.Errorf("unknown user command %q (available: create, list, delete)", args[0])
	}
}

func userCreate(ctx context.Context, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	email := fs.String("email", "", "login email (required)")
	name := fs.String("name", "", "display name")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from stdin instead of prompting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("usage: skalid user create --email <address> [--name <display>] [--password-stdin]")
	}

	password, err := readPassword(*passwordStdin)
	if err != nil {
		return err
	}

	user, err := auth.CreateUser(ctx, st, *email, *name, password)
	if err != nil {
		return err
	}
	fmt.Printf("created user %s (%s)\n", user.Email, user.ID)
	return nil
}

// readPassword collects the password without it ending up in shell history:
// interactively via a no-echo double prompt, or from stdin for scripting
// (printf '%s' "$PW" | skalid user create --email a@b.c --password-stdin).
func readPassword(fromStdin bool) (string, error) {
	if fromStdin {
		data, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && data == "" {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		return strings.TrimRight(data, "\r\n"), nil
	}

	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password (use --password-stdin when not on a terminal): %w", err)
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}

func userList(ctx context.Context, st *store.Store) error {
	users, err := st.ListUsers(ctx)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		fmt.Println("no users")
		return nil
	}
	for _, u := range users {
		twoFA := ""
		if u.TwoFactorEnabled {
			twoFA = "  [2fa]"
		}
		fmt.Printf("%s  %s  created %s%s\n", u.ID, u.Email, u.CreatedAt.Format("2006-01-02"), twoFA)
	}
	return nil
}

func userDelete(ctx context.Context, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("user delete", flag.ContinueOnError)
	email := fs.String("email", "", "login email (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("usage: skalid user delete --email <address>")
	}

	n, err := st.DeleteUserByEmail(ctx, *email)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no user with email %q", *email)
	}
	fmt.Printf("deleted user %s\n", *email)
	return nil
}
