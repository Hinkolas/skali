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
	versionpkg "github.com/Hinkolas/skali/internal/version"
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
	var email string
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "add <name> <host-or-url>",
		Short: "Add a remote and log in to it",
		Long: `Add a named remote for a skali master and perform the initial login,
like "skali remote add example https://skali.example.com". A bare hostname
tries https then http and targets the cluster's /api path
(skali.example.com becomes https://skali.example.com/api); an explicit URL
is used verbatim. On success the new remote becomes the current one; on
failure nothing is stored.

In a terminal the login opens the web console in your browser and waits
for you to approve it there; --no-browser (or SKALI_NO_BROWSER=1) and
non-interactive runs ask for email and password on the terminal instead.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			remoteName := args[0]
			if strings.Contains(remoteName, "://") {
				return fmt.Errorf("the name comes first: `skali remote add <name> %s`", remoteName)
			}
			if remoteName == localRemoteName {
				return errors.New("remote name \"local\" is reserved for the local dev platform; it is managed by skali dev")
			}
			candidates, err := masterCandidates(args[1])
			if err != nil {
				return err
			}
			if cfg.Remotes[remoteName] != nil {
				return fmt.Errorf("remote %q already exists; run `skali remote login %s` to re-authenticate, or pick another name", remoteName, remoteName)
			}
			// Probe before prompting so a typo'd URL never asks for a
			// password. /healthz is unauthenticated on every skali master;
			// the first candidate that answers like one wins.
			master := ""
			var probeErr error
			for _, candidate := range candidates {
				if err := client.New(candidate, "", userAgent()).Health(cmd.Context()); err != nil {
					if probeErr == nil {
						probeErr = fmt.Errorf("master %s is not reachable: %w", candidate, err)
					}
					continue
				}
				master = candidate
				break
			}
			if master == "" {
				if len(candidates) > 1 {
					return fmt.Errorf("%w (also tried %s)", probeErr, candidates[1])
				}
				return probeErr
			}
			sess, instance, err := loginRemote(cmd.Context(), cliprompt.New(os.Stdin, os.Stderr), master, email, noBrowser)
			if err != nil {
				return fmt.Errorf("remote %q not added: %w", remoteName, err)
			}
			cfg.Remotes[remoteName] = &cliconfig.Remote{Master: master, Token: sess.Token, Instance: instance}
			cfg.CurrentRemote = remoteName
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("logged in to %s as %s (remote %q)\n", master, sess.User.Email, remoteName)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "login email (prompted when omitted; implies --no-browser)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "log in with email and password on the terminal instead of the browser")
	return cmd
}

func newRemoteLoginCmd() *cobra.Command {
	var email string
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "login [name]",
		Short: "Log in again (browser, or email and password with --no-browser)",
		Long: `Re-authenticate an existing remote and store the fresh session token.
Without a name the current remote is used; with one, that remote becomes
current when the login succeeds. Remotes are created with "skali remote add".

In a terminal the login opens the web console in your browser and waits
for you to approve it there; --no-browser (or SKALI_NO_BROWSER=1) and
non-interactive runs ask for email and password on the terminal instead.`,
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
				if name == localRemoteName {
					return errors.New("remote \"local\" is managed by skali dev; running `skali dev` logs in to the local platform itself")
				}
				target = cfg.Remotes[name]
				if target == nil {
					return fmt.Errorf("remote %q does not exist; run `skali remote add %s <url>`", name, name)
				}
			} else {
				name, target, err = cfg.Current()
				if err != nil {
					return err
				}
			}
			// The trust decision comes before the credentials: an unpinned
			// probe fetches the identity the master answers with today, and
			// a change (the cluster was reinstalled) must be confirmed. An
			// unreachable master skips the probe; the login surfaces it.
			prompts := cliprompt.New(os.Stdin, os.Stderr)
			if target.Instance != "" {
				probe := client.New(target.Master, "", userAgent())
				_ = probe.Health(cmd.Context())
				observed := probe.ObservedInstance()
				if observed != "" && observed != target.Instance {
					trusted, err := prompts.Confirm(cmd.Context(), cliprompt.ConfirmOptions{
						Title: fmt.Sprintf("The installation identity of remote %q has changed", name),
						Description: "The cluster was probably uninstalled and reinstalled. " +
							"Trust the new installation and log in to it?",
					})
					if err != nil {
						return err
					}
					if !trusted {
						return fmt.Errorf("login aborted; run `skali remote remove %s` to drop this remote", name)
					}
				}
			}
			sess, instance, err := loginRemote(cmd.Context(), prompts, target.Master, email, noBrowser)
			if err != nil {
				return err
			}
			target.Token = sess.Token
			if instance != "" {
				target.Instance = instance
			}
			cfg.CurrentRemote = name
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("logged in to %s as %s (remote %q)\n", target.Master, sess.User.Email, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "login email (prompted when omitted; implies --no-browser)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "log in with email and password on the terminal instead of the browser")
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
				if name == localRemoteName {
					return errors.New("remote \"local\" is managed by skali dev; run `skali dev reset` to remove the local platform")
				}
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
			c := remoteClient(cfg, target)
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
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List remotes",
		Args:    cobra.NoArgs,
		RunE:    runRemoteList,
	}
}

func runRemoteList(cmd *cobra.Command, args []string) error {
	cfg, err := cliconfig.Load()
	if err != nil {
		return err
	}
	// The dev-owned local remote is an implementation detail of skali dev;
	// listing it would invite selecting it.
	names := make([]string, 0, len(cfg.Remotes))
	for name := range cfg.Remotes {
		if name != localRemoteName {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		fmt.Println("no remotes; run `skali remote add <name> <url>`")
		return nil
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
			if name == localRemoteName {
				return errors.New("remote \"local\" is managed by skali dev; dev commands target the local platform themselves")
			}
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
				if mismatch, ok := errors.AsType[*client.InstanceMismatchError](err); ok {
					fmt.Println("health:  ok, but the installation identity changed (cluster reinstalled?)")
					fmt.Printf("         pinned %s, server answers %s\n", mismatch.Pinned, mismatch.Observed)
					fmt.Printf("         trust it with `skali remote login %s` or drop it with `skali remote remove %s`\n", name, name)
					return nil
				}
				fmt.Printf("health:  unreachable (%v)\n", err)
				return nil
			}
			fmt.Println("health:  ok")
			if version := c.ObservedVersion(); version != "" {
				if version == versionpkg.Version {
					fmt.Printf("server:  skalid %s\n", version)
				} else {
					fmt.Printf("server:  skalid %s (this CLI is %s)\n", version, versionpkg.Version)
				}
			}

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
			if info.User.Role != "" {
				role := info.User.Role
				if info.User.Role != "admin" && info.User.CreateProjects {
					role += " (may create projects)"
				}
				fmt.Printf("role:    %s\n", role)
			}
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
			"  curl -H \"Authorization: Bearer $(skali remote token)\" https://skali.example.com/api/v1/projects\n\n" +
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
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove a remote and revoke its session",
		Args:    cobra.ExactArgs(1),
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
				c := remoteClient(cfg, target)
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

// masterCandidates resolves the add argument into the base URLs to probe.
// An explicit URL is used verbatim. A bare hostname gets the convenient
// form: https first with http as the fallback, and the /api path a cluster
// serves its API under (the daemon strips /api itself, so the suffix also
// works against a directly exposed daemon). A schemeless input carrying a
// path keeps that path instead.
func masterCandidates(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.Contains(trimmed, "://") {
		master, err := parseMasterURL(trimmed)
		if err != nil {
			return nil, err
		}
		return []string{master}, nil
	}
	u, parseErr := url.Parse("https://" + trimmed)
	if parseErr != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid master %q: expected a hostname like skali.example.com or a full URL", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("invalid master %q: credentials do not belong in the URL", raw)
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" {
		path = "/api"
	}
	return []string{"https://" + u.Host + path, "http://" + u.Host + path}, nil
}

// parseMasterURL validates an explicit master URL.
func parseMasterURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	u, parseErr := url.Parse(trimmed)
	if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid master URL %q: expected something like https://skali.example.com", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("invalid master URL %q: credentials do not belong in the URL", raw)
	}
	return strings.TrimRight(trimmed, "/"), nil
}

// loginRemote picks the login path: the browser when the terminal is
// interactive and nobody opted out (an explicit --email means the person
// wants the typed path), the prompts otherwise. A master without the
// device flow (an older daemon answers 404) falls back to the prompts.
func loginRemote(ctx context.Context, prompts *cliprompt.Session, master, email string, noBrowser bool) (*client.SessionCreated, string, error) {
	if email == "" && browserAuthAvailable(noBrowser) {
		sess, instance, err := deviceLogin(ctx, os.Stderr, master)
		if !client.IsNotFound(err) {
			return sess, instance, err
		}
	}
	return loginSession(ctx, prompts, master, email)
}

// loginSession collects credentials (the email is prompted unless provided),
// performs the login, and answers a TOTP challenge when one is presented.
// Alongside the session it returns the installation identity the master
// answered with (empty from an older daemon) so callers can pin it. The
// prompt session comes from the caller so questions asked before the login
// (the identity trust confirm) share one stdin reader with these.
func loginSession(ctx context.Context, prompts *cliprompt.Session, master, email string) (*client.SessionCreated, string, error) {
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
			return nil, "", fmt.Errorf("read email: %w", err)
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
		return nil, "", fmt.Errorf("read password: %w", err)
	}

	c := client.New(master, "", userAgent())
	res, err := c.Login(ctx, email, password)
	if err != nil {
		return nil, "", err
	}
	sess := res.Session
	if res.Challenge != nil {
		code, err := prompts.Text(ctx, cliprompt.TextOptions{
			Title:       "Two-factor code",
			Description: "Enter the six-digit code from your authenticator.",
			CharLimit:   6,
			Validate:    validateTOTPCode,
		})
		if err != nil {
			return nil, "", fmt.Errorf("read code: %w", err)
		}
		sess, err = c.VerifyTwoFactor(ctx, res.Challenge.Token, code)
		if err != nil {
			return nil, "", err
		}
	}
	return sess, c.ObservedInstance(), nil
}
