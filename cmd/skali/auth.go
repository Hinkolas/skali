package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Log in and out of a skali master",
	}
	cmd.AddCommand(newAuthLoginCmd(), newAuthLogoutCmd(), newAuthWhoamiCmd(), newAuthStatusCmd(), newAuthTokenCmd())
	return cmd
}

func newAuthTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print the stored session token (docker login password for the managed registry)",
		Long: "Print the current context's session token to stdout, for example:\n\n" +
			"  skali auth token | docker login registry.example.com -u you@example.com --password-stdin",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			name, context, err := cfg.Current()
			if err != nil {
				return err
			}
			if context.Token == "" {
				return fmt.Errorf("not logged in on context %q; run `skali auth login` first", name)
			}
			fmt.Println(context.Token)
			return nil
		},
	}
}

func newAuthLoginCmd() *cobra.Command {
	var master, contextName, email string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in with email and password (and TOTP when enabled)",
		Long: `Log in to a skali master and store the session token in the active context.

With --master, the context is created or updated (named by --context,
default "default") and made current. Without it, the active context's
master is used.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}

			// Resolve the target context: --master upserts one, otherwise the
			// current context must exist.
			name := contextName
			switch {
			case master != "":
				if name == "" {
					name = cfg.CurrentContext
					if name == "" {
						name = "default"
					}
				}
				if cfg.Contexts[name] == nil {
					cfg.Contexts[name] = &cliconfig.Context{}
				}
				cfg.Contexts[name].Master = strings.TrimRight(master, "/")
				cfg.CurrentContext = name
			case name != "":
				if cfg.Contexts[name] == nil {
					return fmt.Errorf("context %q does not exist; add it with --master or `skali context add`", name)
				}
				cfg.CurrentContext = name
			default:
				name, _, err = cfg.Current()
				if err != nil {
					return err
				}
			}
			target := cfg.Contexts[name]

			// Collect credentials. One shared reader so buffered lines are
			// never lost between prompts (matters for piped stdin).
			stdin := bufio.NewReader(os.Stdin)
			if email == "" {
				line, err := promptLine(stdin, "Email: ")
				if err != nil {
					return fmt.Errorf("read email: %w", err)
				}
				email = line
			}
			password, err := promptSecret(stdin, "Password: ")
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}

			c := client.New(target.Master, "", userAgent())
			res, err := c.Login(cmd.Context(), email, password)
			if err != nil {
				return err
			}

			sess := res.Session
			if res.Challenge != nil {
				code, err := promptLine(stdin, "Two-factor code: ")
				if err != nil {
					return fmt.Errorf("read code: %w", err)
				}
				sess, err = c.VerifyTwoFactor(cmd.Context(), res.Challenge.Token, code)
				if err != nil {
					return err
				}
			}

			target.Token = sess.Token
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("logged in to %s as %s (context %q)\n", target.Master, sess.User.Email, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&master, "master", "", "master URL, e.g. http://localhost:7070 (creates/updates the context)")
	cmd.Flags().StringVar(&contextName, "context", "", "context to log in to (default: current, or \"default\" with --master)")
	cmd.Flags().StringVar(&email, "email", "", "login email (prompted when omitted)")
	return cmd
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Revoke the current session and forget its token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, name, c, err := currentClient()
			if err != nil {
				return err
			}
			ctx := cfg.Contexts[name]
			if ctx.Token == "" {
				fmt.Println("not logged in")
				return nil
			}
			// Best effort server-side; the local token is cleared regardless,
			// so an unreachable master can't keep you "logged in".
			if err := c.Logout(cmd.Context()); err != nil {
				fmt.Fprintf(os.Stderr, "warning: server-side revoke failed: %v\n", err)
			}
			ctx.Token = ""
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("logged out of context %q\n", name)
			return nil
		},
	}
}

func newAuthWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the authenticated user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, name, c, err := currentClient()
			if err != nil {
				return err
			}
			info, err := c.CurrentSession(cmd.Context())
			if err != nil {
				return whoamiError(name, err)
			}
			fmt.Printf("%s", info.User.Email)
			if info.User.Name != "" {
				fmt.Printf(" (%s)", info.User.Name)
			}
			fmt.Printf("\ncontext:        %s\nsession expires: %s\n", name, info.Session.ExpiresAt.Local().Format("2006-01-02 15:04"))
			return nil
		},
	}
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show context, master reachability, and session validity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, name, c, err := currentClient()
			if err != nil {
				return err
			}
			ctx := cfg.Contexts[name]
			fmt.Printf("context: %s\nmaster:  %s\n", name, ctx.Master)

			if err := c.Health(cmd.Context()); err != nil {
				fmt.Printf("master:  unreachable (%v)\n", err)
				return nil
			}
			fmt.Println("health:  ok")

			if ctx.Token == "" {
				fmt.Println("session: not logged in")
				return nil
			}
			info, err := c.CurrentSession(cmd.Context())
			if err != nil {
				var apiErr *client.APIError
				if errors.As(err, &apiErr) && apiErr.Code == "invalid_token" {
					fmt.Println("session: expired or revoked; run `skali auth login`")
					return nil
				}
				return err
			}
			fmt.Printf("session: %s, expires %s\n", info.User.Email, info.Session.ExpiresAt.Local().Format("2006-01-02 15:04"))
			return nil
		},
	}
}

// promptLine prints a prompt to stderr and reads one trimmed line.
func promptLine(r *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptSecret reads without echo on a terminal, and falls back to a plain
// line read when stdin is piped (scripts, CI).
func promptSecret(r *bufio.Reader, prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return promptLine(r, prompt)
	}
	fmt.Fprint(os.Stderr, prompt)
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(secret), nil
}

// whoamiError turns a dead session into a friendly hint instead of a raw 401.
func whoamiError(context string, err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.Code == "invalid_token" {
		return fmt.Errorf("not logged in to context %q; run `skali auth login`", context)
	}
	return err
}
