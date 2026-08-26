package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

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
		return errors.New("usage: skalid user <create|list|set-role|reset-password|delete> [flags]")
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
	case "reset-password":
		return userResetPassword(ctx, st, args[1:])
	case "delete":
		return userDelete(ctx, st, args[1:])
	default:
		return fmt.Errorf("unknown user command %q (available: create, list, set-role, reset-password, delete)", args[0])
	}
}

func userCreate(ctx context.Context, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	email := fs.String("email", "", "login email (required)")
	name := fs.String("name", "", "display name")
	role := fs.String("role", auth.RoleMember, "instance role: admin or member (the first user needs admin)")
	createProjects := fs.Bool("create-projects", false, "let a member create projects (they become admin of what they create)")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from stdin instead of prompting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("usage: skalid user create --email <address> [--name <display>] [--role admin|member] [--create-projects] [--password-stdin]")
	}

	password, err := readPassword(ctx, *passwordStdin)
	if err != nil {
		return err
	}

	user, err := auth.CreateUser(ctx, st, *email, *name, password, *role)
	if err != nil {
		return err
	}
	if *createProjects {
		updated, err := auth.SetUserCreateProjects(ctx, st, user.ID, true)
		if err != nil {
			return err
		}
		user = &updated
	}
	fmt.Printf("created %s %s (%s)%s\n", user.Role, user.Email, user.ID, createProjectsMarker(*user))
	return nil
}

// createProjectsMarker flags members who may create projects; admins always
// may, so the marker would be noise on them.
func createProjectsMarker(user store.User) string {
	if user.Role != auth.RoleAdmin && user.CreateProjects {
		return "  [creates projects]"
	}
	return ""
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

// userResetPassword is the operator recovery path for a forgotten password:
// no old password, no caller identity, only database access. `skali cluster
// reset-password` runs it as a one-shot job on the cluster. Every session of
// the user is revoked; --disable-2fa also drops a lost authenticator.
func userResetPassword(ctx context.Context, st *store.Store, args []string) error {
	fs := flag.NewFlagSet("user reset-password", flag.ContinueOnError)
	email := fs.String("email", "", "login email (required)")
	passwordStdin := fs.Bool("password-stdin", false, "read the new password from stdin instead of prompting")
	disableTwoFactor := fs.Bool("disable-2fa", false, "also remove the user's two-factor enrollment")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("usage: skalid user reset-password --email <address> [--disable-2fa] [--password-stdin]")
	}

	user, err := st.GetUserByEmail(ctx, *email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no user with email %q", *email)
		}
		return err
	}
	password, err := readPassword(ctx, *passwordStdin)
	if err != nil {
		return err
	}
	if err := auth.ResetUserPassword(ctx, st, user.ID, password); err != nil {
		return err
	}
	if *disableTwoFactor {
		if err := auth.ClearUserTwoFactor(ctx, st, user.ID); err != nil {
			return err
		}
	}
	suffix := ""
	if *disableTwoFactor {
		suffix = ", two-factor disabled"
	}
	fmt.Printf("password reset for %s %s (%s), sessions revoked%s\n", user.Role, user.Email, user.ID, suffix)
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
		fmt.Printf("%s  %-6s  %s  created %s%s%s\n", u.ID, u.Role, u.Email, u.CreatedAt.Format("2006-01-02"), twoFA, createProjectsMarker(u))
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

	user, err := st.GetUserByEmail(ctx, *email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no user with email %q", *email)
		}
		return err
	}
	// Through the service so the last-admin guard applies: the operator
	// CLI is the recovery path, not a way around it.
	if err := auth.DeleteUser(ctx, st, user.ID); err != nil {
		if errors.Is(err, auth.ErrLastAdmin) {
			return errors.New("refusing to delete the last admin; promote another user first")
		}
		return err
	}
	fmt.Printf("deleted user %s\n", *email)
	return nil
}
