package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/config"
	"github.com/Hinkolas/skali/internal/store"
)

// runUser implements the operator CLI for app users. It loads only
// config.Base: user management needs DATABASE_URL and must not demand
// AUTH_SECRET or other serve-only settings.
func runUser(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: skalid user <create|list|set-role|delete> [flags]")
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
	case "set-role":
		return userSetRole(ctx, st, args[1:])
	case "delete":
		return userDelete(ctx, st, args[1:])
	default:
		return fmt.Errorf("unknown user command %q (available: create, list, set-role, delete)", args[0])
	}
}

func userCreate(ctx context.Context, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	email := fs.String("email", "", "login email (required)")
	name := fs.String("name", "", "display name")
	role := fs.String("role", auth.RoleMember, "instance role: admin or member (the first user needs admin)")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from stdin instead of prompting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("usage: skalid user create --email <address> [--name <display>] [--role admin|member] [--password-stdin]")
	}

	password, err := readPassword(ctx, *passwordStdin)
	if err != nil {
		return err
	}

	user, err := auth.CreateUser(ctx, st, *email, *name, password, *role)
	if err != nil {
		return err
	}
	fmt.Printf("created %s %s (%s)\n", user.Role, user.Email, user.ID)
	return nil
}

// userSetRole is the operator override for role changes — unlike the API it
// has no caller identity, so only the last-admin guard applies. It exists so
// a lost-admin instance is recoverable from the server itself.
func userSetRole(ctx context.Context, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("user set-role", flag.ContinueOnError)
	email := fs.String("email", "", "login email (required)")
	role := fs.String("role", "", "instance role: admin or member (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" || *role == "" {
		return errors.New("usage: skalid user set-role --email <address> --role admin|member")
	}

	user, err := st.GetUserByEmail(ctx, *email)
	if err != nil {
		return fmt.Errorf("no user with email %q", *email)
	}
	user, err = auth.SetUserRole(ctx, st, user.ID, *role)
	if err != nil {
		return err
	}
	fmt.Printf("%s is now %s\n", user.Email, user.Role)
	return nil
}

// readPassword collects the password without it ending up in shell history:
// interactively via a no-echo double prompt, or from stdin for scripting
// (printf '%s' "$PW" | skalid user create --email a@b.c --password-stdin).
func readPassword(ctx context.Context, fromStdin bool) (string, error) {
	if fromStdin {
		data, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && data == "" {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		return strings.TrimRight(data, "\r\n"), nil
	}

	prompts := cliprompt.New(os.Stdin, os.Stderr)
	if !prompts.Interactive() {
		return "", errors.New("read password (use --password-stdin when not on a terminal)")
	}
	first, err := prompts.Secret(ctx, cliprompt.SecretOptions{
		Title: "Password",
		Validate: func(value string) error {
			if value == "" {
				return errors.New("password must not be empty")
			}
			return nil
		},
	})
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	second, err := prompts.Secret(ctx, cliprompt.SecretOptions{
		Title: "Repeat password",
		Validate: func(value string) error {
			if value != first {
				return errors.New("passwords do not match")
			}
			return nil
		},
	})
	if err != nil {
		return "", err
	}
	return second, nil
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
		fmt.Printf("%s  %-6s  %s  created %s%s\n", u.ID, u.Role, u.Email, u.CreatedAt.Format("2006-01-02"), twoFA)
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
