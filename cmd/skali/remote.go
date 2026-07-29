package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
)

func newRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Manage the skali masters this machine talks to",
		Long: `Manage named remotes: the skali masters this machine can talk to, each
with its own session. Bare "skali remote" lists them; "skali remote add"
creates one and logs in; "skali remote login" re-authenticates.`,
		Args: cobra.NoArgs,
		RunE: runRemoteList,
	}
	cmd.AddCommand(newRemoteAddCmd(), newRemoteLoginCmd(), newRemoteLogoutCmd(),
		newRemoteListCmd(), newRemoteUseCmd(), newRemoteStatusCmd(),
		newRemoteTokenCmd(), newRemoteRemoveCmd())
	return cmd
}

func newRemoteAddCmd() *cobra.Command {
	var name, email string
	cmd := &cobra.Command{
		Use:   "add <url>",
		Short: "Add a remote and log in to it",
		Long: `Add a named remote for a skali master and perform the initial login.
The name defaults to the URL host (https://skali.example.com becomes
"skali.example.com"); override it with --name. On success the new remote
becomes the current one; on failure nothing is stored.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			derived, master, err := parseMasterURL(args[0])
			if err != nil {
				return err
			}
			remoteName := name
			if remoteName == "" {
				remoteName = derived
			}
			if remoteName == localRemoteName {
				if name == "" {
					return fmt.Errorf("the derived name %q is reserved for the local dev platform; pick one with --name", derived)
				}
				return errors.New("remote name \"local\" is reserved for the local dev platform; it is managed by skali dev")
			}
			if cfg.Remotes[remoteName] != nil {
				return fmt.Errorf("remote %q already exists; run `skali remote login %s` to re-authenticate, or pick another name with --name", remoteName, remoteName)
			}
			// Probe before prompting so a typo'd URL never asks for a
			// password. /healthz is unauthenticated on every skali master.
			if err := client.New(master, "", userAgent()).Health(cmd.Context()); err != nil {
				return fmt.Errorf("master %s is not reachable: %w", master, err)
			}
			sess, err := loginSession(cmd.Context(), master, email)
			if err != nil {
				return fmt.Errorf("remote %q not added: %w", remoteName, err)
			}
			cfg.Remotes[remoteName] = &cliconfig.Remote{Master: master, Token: sess.Token}
			cfg.CurrentRemote = remoteName
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("logged in to %s as %s (remote %q)\n", master, sess.User.Email, remoteName)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "remote name (default: the URL host)")
	cmd.Flags().StringVar(&email, "email", "", "login email (prompted when omitted)")
	return cmd
}

func newRemoteLoginCmd() *cobra.Command {
	var email string
	cmd := &cobra.Command{
		Use:   "login [name]",
		Short: "Log in again with email and password (and TOTP when enabled)",
		Long: `Re-authenticate an existing remote and store the fresh session token.
Without a name the current remote is used; with one, that remote becomes
current when the login succeeds. Remotes are created with "skali remote add".`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			var name string
			var target *cliconfig.Remote
			if len(args) == 1 {
				name = args[0]
				target = cfg.Remotes[name]
				if target == nil {
					return fmt.Errorf("remote %q does not exist; run `skali remote add <url>`", name)
				}
			} else {
				name, target, err = cfg.Current()
				if err != nil {
					return err
				}
			}
			sess, err := loginSession(cmd.Context(), target.Master, email)
			if err != nil {
				return err
			}
			target.Token = sess.Token
			cfg.CurrentRemote = name
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("logged in to %s as %s (remote %q)\n", target.Master, sess.User.Email, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "login email (prompted when omitted)")
	return cmd
}

func newRemoteLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout [name]",
		Short: "Revoke a remote's session and forget its token",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			var name string
			var target *cliconfig.Remote
			if len(args) == 1 {
				name = args[0]
				target = cfg.Remotes[name]
				if target == nil {
					return fmt.Errorf("remote %q does not exist; run `skali remote list`", name)
				}
			} else {
				name, target, err = cfg.Current()
				if err != nil {
					return err
				}
			}
			if target.Token == "" {
				fmt.Println("not logged in")
				return nil
			}
			// Best effort server-side; the local token is cleared regardless,
			// so an unreachable master can't keep you "logged in".
			c := client.New(target.Master, target.Token, userAgent())
			if err := c.Logout(cmd.Context()); err != nil {
				fmt.Fprintf(os.Stderr, "warning: server-side revoke failed: %v\n", err)
			}
			target.Token = ""
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("logged out of remote %q\n", name)
			return nil
		},
	}
}

func newRemoteListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List remotes",
		Args:  cobra.NoArgs,
		RunE:  runRemoteList,
	}
}

func runRemoteList(cmd *cobra.Command, args []string) error {
	cfg, err := cliconfig.Load()
	if err != nil {
		return err
	}
	if len(cfg.Remotes) == 0 {
		fmt.Println("no remotes; run `skali remote add <url>`")
		return nil
	}
	names := make([]string, 0, len(cfg.Remotes))
	for name := range cfg.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		marker := " "
		if name == cfg.CurrentRemote {
			marker = "*"
		}
		loggedIn := ""
		if cfg.Remotes[name].Token != "" {
			loggedIn = "  [logged in]"
		}
		fmt.Printf("%s %s  %s%s\n", marker, name, cfg.Remotes[name].Master, loggedIn)
	}
	return nil
}

func newRemoteUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Switch the current remote",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			name := args[0]
			if cfg.Remotes[name] == nil {
				return fmt.Errorf("remote %q does not exist; run `skali remote list`", name)
			}
			cfg.CurrentRemote = name
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("switched to remote %q\n", name)
			return nil
		},
	}
}

func newRemoteStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the current remote, master reachability, session, and user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, name, c, err := currentClient()
			if err != nil {
				return err
			}
			remote := cfg.Remotes[name]
			fmt.Printf("remote:  %s\nmaster:  %s\n", name, remote.Master)

			if err := c.Health(cmd.Context()); err != nil {
				fmt.Printf("health:  unreachable (%v)\n", err)
				return nil
			}
			fmt.Println("health:  ok")

			if remote.Token == "" {
				fmt.Println("session: not logged in")
				return nil
			}
			info, err := c.CurrentSession(cmd.Context())
			if err != nil {
				var apiErr *client.APIError
				if errors.As(err, &apiErr) && apiErr.Code == "invalid_token" {
					fmt.Println("session: expired or revoked; run `skali remote login`")
					return nil
				}
				return err
			}
			user := info.User.Email
			if info.User.Name != "" {
				user += " (" + info.User.Name + ")"
			}
			fmt.Printf("user:    %s\n", user)
			fmt.Printf("session: valid, expires %s\n", info.Session.ExpiresAt.Local().Format("2006-01-02 15:04"))
			return nil
		},
	}
}

func newRemoteTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print the stored session token (for scripting API requests)",
		Long: "Print the current remote's session token to stdout, for example:\n\n" +
			"  curl -H \"Authorization: Bearer $(skali remote token)\" https://skali.example.com/v1/projects\n\n" +
			"Deploys authenticate to the managed registry automatically with this\n" +
			"session; docker login is only needed for third-party OCI tooling, with\n" +
			"any username and the token as the password.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			name, remote, err := cfg.Current()
			if err != nil {
				return err
			}
			if remote.Token == "" {
				return fmt.Errorf("not logged in on remote %q; run `skali remote login` first", name)
			}
			fmt.Println(remote.Token)
			return nil
		},
	}
}

func newRemoteRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a remote and revoke its session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == localRemoteName {
				return errors.New("remote \"local\" is managed by skali dev; run `skali dev reset` to remove the local platform")
			}
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			target := cfg.Remotes[name]
			if target == nil {
				return fmt.Errorf("remote %q does not exist; run `skali remote list`", name)
			}
			if target.Token != "" {
				// Best effort, like logout: removal must not strand a live
				// session server-side, but an unreachable master cannot
				// block the removal either.
				c := client.New(target.Master, target.Token, userAgent())
				if err := c.Logout(cmd.Context()); err != nil {
					fmt.Fprintf(os.Stderr, "warning: server-side revoke failed: %v\n", err)
				}
			}
			delete(cfg.Remotes, name)
			cleared := cfg.CurrentRemote == name
			if cleared {
				cfg.CurrentRemote = ""
			}
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("removed remote %q\n", name)
			if cleared && len(cfg.Remotes) > 0 {
				fmt.Println("no remote selected; run `skali remote use <name>`")
			}
			return nil
		},
	}
}

// parseMasterURL validates a master URL and derives the default remote name
// from its host, including any non-standard port (http://localhost:7070
// becomes "localhost:7070").
func parseMasterURL(raw string) (name, master string, err error) {
	trimmed := strings.TrimSpace(raw)
	u, parseErr := url.Parse(trimmed)
	if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", fmt.Errorf("invalid master URL %q: expected something like https://skali.example.com", raw)
	}
	if u.User != nil {
		return "", "", fmt.Errorf("invalid master URL %q: credentials do not belong in the URL", raw)
	}
	return strings.ToLower(u.Host), strings.TrimRight(trimmed, "/"), nil
}

// loginSession collects credentials (the email is prompted unless provided),
// performs the login, and answers a TOTP challenge when one is presented.
func loginSession(ctx context.Context, master, email string) (*client.SessionCreated, error) {
	prompts := cliprompt.New(os.Stdin, os.Stderr)
	if email == "" {
		line, err := prompts.Text(ctx, cliprompt.TextOptions{
			Title: "Email",
			Validate: func(value string) error {
				if value == "" {
					return errors.New("email is required")
				}
				return nil
			},
		})
		if err != nil {
			return nil, fmt.Errorf("read email: %w", err)
		}
		email = line
	}
	password, err := prompts.Secret(ctx, cliprompt.SecretOptions{
		Title: "Password",
		Validate: func(value string) error {
			if value == "" {
				return errors.New("password is required")
			}
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("read password: %w", err)
	}

	c := client.New(master, "", userAgent())
	res, err := c.Login(ctx, email, password)
	if err != nil {
		return nil, err
	}
	sess := res.Session
	if res.Challenge != nil {
		code, err := prompts.Text(ctx, cliprompt.TextOptions{
			Title:       "Two-factor code",
			Description: "Enter the six-digit code from your authenticator.",
			CharLimit:   6,
			Validate: func(value string) error {
				if len(value) != 6 {
					return errors.New("enter a six-digit code")
				}
				for _, digit := range value {
					if digit < '0' || digit > '9' {
						return errors.New("enter a six-digit code")
					}
				}
				return nil
			},
		})
		if err != nil {
			return nil, fmt.Errorf("read code: %w", err)
		}
		sess, err = c.VerifyTwoFactor(ctx, res.Challenge.Token, code)
		if err != nil {
			return nil, err
		}
	}
	return sess, nil
}
