package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/cliconfig"
)

// runCapturingStdout executes fn with os.Stdout redirected into the
// returned string; the remote commands print with fmt.Printf directly.
func runCapturingStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	require.NoError(t, err)
	previous := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = previous }()
	runErr := fn()
	require.NoError(t, write.Close())
	output, err := io.ReadAll(read)
	require.NoError(t, err)
	return string(output), runErr
}

// withStdin replaces os.Stdin with a pipe preloaded with input; a pipe is
// not a terminal, so promptSecret takes its deterministic plain-read path.
func withStdin(t *testing.T, input string, fn func() error) error {
	t.Helper()
	read, write, err := os.Pipe()
	require.NoError(t, err)
	_, err = write.WriteString(input)
	require.NoError(t, err)
	require.NoError(t, write.Close())
	previous := os.Stdin
	os.Stdin = read
	defer func() { os.Stdin = previous }()
	return fn()
}

// execute runs a command factory with explicit args so cobra never falls
// back to the test binary's os.Args.
func execute(cmd *cobra.Command, args ...string) error {
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if args == nil {
		args = []string{}
	}
	cmd.SetArgs(args)
	return cmd.Execute()
}

func seedConfig(t *testing.T, cfg *cliconfig.Config) {
	t.Helper()
	require.NoError(t, cliconfig.Save(cfg))
}

func loadConfig(t *testing.T) *cliconfig.Config {
	t.Helper()
	cfg, err := cliconfig.Load()
	require.NoError(t, err)
	return cfg
}

func fakeMaster(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// deadURL returns a URL nothing listens on.
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	return url
}

const sessionJSON = `{"token":"tok-1","expires_at":"2026-07-29T14:02:00Z",` +
	`"user":{"email":"dana@example.com","name":"Dana"}}`

func TestRemoteTokenCommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "dev",
		Remotes: map[string]*cliconfig.Remote{
			"dev": {Master: "http://skali.localhost:7070", Token: "session-token-value"},
		},
	})

	output, err := runCapturingStdout(t, func() error {
		return execute(newRemoteTokenCommand())
	})
	require.NoError(t, err)
	require.Equal(t, "session-token-value\n", output)
}

func TestRemoteTokenCommandNotLoggedIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "dev",
		Remotes: map[string]*cliconfig.Remote{
			"dev": {Master: "http://skali.localhost:7070"},
		},
	})

	err := execute(newRemoteTokenCommand())
	require.ErrorContains(t, err, "not logged in")
	require.ErrorContains(t, err, "skali remote login")
}

func TestParseMasterURL(t *testing.T) {
	cases := []struct {
		raw    string
		master string
		fails  bool
	}{
		{raw: "https://skali.khz.dev", master: "https://skali.khz.dev"},
		{raw: "https://skali.khz.dev/", master: "https://skali.khz.dev"},
		// The single-surface cluster API lives behind a path prefix; the
		// path survives round-tripping.
		{raw: "https://skali.khz.dev/api", master: "https://skali.khz.dev/api"},
		{raw: "https://skali.khz.dev/api/", master: "https://skali.khz.dev/api"},
		{raw: "http://localhost:7070", master: "http://localhost:7070"},
		{raw: "https://SKALI.Example.Com", master: "https://SKALI.Example.Com"},
		{raw: "skali.khz.dev", fails: true},
		{raw: "", fails: true},
		{raw: "https://", fails: true},
		{raw: "ftp://skali.khz.dev", fails: true},
		{raw: "https://user:pw@skali.khz.dev", fails: true},
	}
	for _, tc := range cases {
		master, err := parseMasterURL(tc.raw)
		if tc.fails {
			require.Error(t, err, tc.raw)
			continue
		}
		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.master, master, tc.raw)
	}
}

func TestMasterCandidates(t *testing.T) {
	cases := []struct {
		raw        string
		candidates []string
		fails      bool
	}{
		// Bare hostnames get https-then-http and the cluster's /api path.
		{raw: "skali.khz.dev",
			candidates: []string{"https://skali.khz.dev/api", "http://skali.khz.dev/api"}},
		{raw: "skali.khz.dev/",
			candidates: []string{"https://skali.khz.dev/api", "http://skali.khz.dev/api"}},
		{raw: "localhost:7070",
			candidates: []string{"https://localhost:7070/api", "http://localhost:7070/api"}},
		// A schemeless input carrying a path keeps that path.
		{raw: "skali.khz.dev/custom",
			candidates: []string{"https://skali.khz.dev/custom", "http://skali.khz.dev/custom"}},
		// Explicit URLs are verbatim, single candidate.
		{raw: "https://skali.khz.dev/api",
			candidates: []string{"https://skali.khz.dev/api"}},
		{raw: "http://localhost:7070",
			candidates: []string{"http://localhost:7070"}},
		{raw: "", fails: true},
		{raw: "user:pw@skali.khz.dev", fails: true},
		{raw: "skali.khz.dev?x=1", fails: true},
		{raw: "ftp://skali.khz.dev", fails: true},
	}
	for _, tc := range cases {
		candidates, err := masterCandidates(tc.raw)
		if tc.fails {
			require.Error(t, err, tc.raw)
			continue
		}
		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.candidates, candidates, tc.raw)
	}
}

func TestRemoteAddBareHostname(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// The fake master serves /api-prefixed paths like a production edge
	// (and like the daemon's own strip wrapper). The https candidate fails
	// against the plain-http listener, so the http fallback must win.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"session":` + sessionJSON + `}`))
	})
	srv := fakeMaster(t, mux)
	host := strings.TrimPrefix(srv.URL, "http://")

	var output string
	err := withStdin(t, "password\n", func() error {
		var runErr error
		output, runErr = runCapturingStdout(t, func() error {
			return execute(newRemoteAddCommand(), "bare", host, "--email", "dana@example.com")
		})
		return runErr
	})
	require.NoError(t, err)
	require.Contains(t, output, "logged in to http://"+host+"/api as dana@example.com")

	cfg := loadConfig(t)
	require.Equal(t, "bare", cfg.CurrentRemote)
	require.Equal(t, "http://"+host+"/api", cfg.Remotes["bare"].Master)
}

func TestRemoteAddReservedName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// The reserved name refuses before any network access, so no server
	// exists here.
	err := execute(newRemoteAddCommand(), "local", "https://skali.example.com")
	require.ErrorContains(t, err, "reserved")
	require.ErrorContains(t, err, "skali dev")

	cfg := loadConfig(t)
	require.Empty(t, cfg.Remotes)
	require.Empty(t, cfg.CurrentRemote)
}

func TestRemoteAddSwappedArguments(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// A URL in the name slot is the old argument order; the error teaches
	// the new one before any network access.
	err := execute(newRemoteAddCommand(), "https://skali.example.com", "example")
	require.ErrorContains(t, err, "the name comes first")
	require.ErrorContains(t, err, "skali remote add <name> https://skali.example.com")
	require.Empty(t, loadConfig(t).Remotes)
}

func TestRemoteAddDuplicate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "skali.example.com",
		Remotes: map[string]*cliconfig.Remote{
			"skali.example.com": {Master: "https://skali.example.com", Token: "tok"},
		},
	})

	// The duplicate check precedes the reachability probe, so the
	// unroutable master is never contacted.
	err := execute(newRemoteAddCommand(), "skali.example.com", "https://skali.example.com")
	require.ErrorContains(t, err, "already exists")
	require.ErrorContains(t, err, "skali remote login skali.example.com")
}

func TestRemoteAddUnreachable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	err := execute(newRemoteAddCommand(), "dead", deadURL(t))
	require.ErrorContains(t, err, "not reachable")

	cfg := loadConfig(t)
	require.Empty(t, cfg.Remotes)
}

func TestRemoteAddSuccess(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"session":` + sessionJSON + `}`))
	})
	srv := fakeMaster(t, mux)

	var output string
	err := withStdin(t, "password\n", func() error {
		var runErr error
		output, runErr = runCapturingStdout(t, func() error {
			return execute(newRemoteAddCommand(), "myremote", srv.URL, "--email", "dana@example.com")
		})
		return runErr
	})
	require.NoError(t, err)
	require.Contains(t, output, "logged in to "+srv.URL+" as dana@example.com")
	require.Contains(t, output, `(remote "myremote")`)

	cfg := loadConfig(t)
	require.Equal(t, "myremote", cfg.CurrentRemote)
	require.Equal(t, srv.URL, cfg.Remotes["myremote"].Master)
	require.Equal(t, "tok-1", cfg.Remotes["myremote"].Token)
}

func TestRemoteAddTwoFactor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	verified := false
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"challenge":{"token":"ch-1","expires_at":"2026-07-22T18:00:00Z"}}`))
	})
	mux.HandleFunc("/v1/auth/2fa/verify", func(w http.ResponseWriter, r *http.Request) {
		verified = true
		_, _ = w.Write([]byte(`{"session":` + sessionJSON + `}`))
	})
	srv := fakeMaster(t, mux)

	err := withStdin(t, "password\n123456\n", func() error {
		_, runErr := runCapturingStdout(t, func() error {
			return execute(newRemoteAddCommand(), "myremote", srv.URL, "--email", "dana@example.com")
		})
		return runErr
	})
	require.NoError(t, err)
	require.True(t, verified)
	require.Equal(t, "tok-1", loadConfig(t).Remotes["myremote"].Token)
}

func TestRemoteAddFailedLoginLeavesNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_credentials","message":"invalid email or password"}}`))
	})
	srv := fakeMaster(t, mux)

	err := withStdin(t, "wrong\n", func() error {
		return execute(newRemoteAddCommand(), "myremote", srv.URL, "--email", "dana@example.com")
	})
	require.ErrorContains(t, err, "not added")
	require.ErrorContains(t, err, "invalid_credentials")

	cfg := loadConfig(t)
	require.Empty(t, cfg.Remotes)
	require.Empty(t, cfg.CurrentRemote)
}

func TestRemoteLoginUnknownName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	err := execute(newRemoteLoginCommand(), "nope")
	require.ErrorContains(t, err, `remote "nope" does not exist`)
	require.ErrorContains(t, err, "skali remote add")
}

func TestRemoteLoginSwitchesCurrentOnSuccess(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"session":` + sessionJSON + `}`))
	})
	srv := fakeMaster(t, mux)
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "a",
		Remotes: map[string]*cliconfig.Remote{
			"a": {Master: "https://a.example.com", Token: "tok-a"},
			"b": {Master: srv.URL, Token: "stale"},
		},
	})

	err := withStdin(t, "password\n", func() error {
		_, runErr := runCapturingStdout(t, func() error {
			return execute(newRemoteLoginCommand(), "b", "--email", "dana@example.com")
		})
		return runErr
	})
	require.NoError(t, err)

	cfg := loadConfig(t)
	require.Equal(t, "b", cfg.CurrentRemote)
	require.Equal(t, "tok-1", cfg.Remotes["b"].Token)
	require.Equal(t, "tok-a", cfg.Remotes["a"].Token)
}

func TestRemoteUse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "a",
		Remotes: map[string]*cliconfig.Remote{
			"a": {Master: "https://a.example.com"},
			"b": {Master: "https://b.example.com"},
		},
	})

	output, err := runCapturingStdout(t, func() error {
		return execute(newRemoteUseCommand(), "b")
	})
	require.NoError(t, err)
	require.Contains(t, output, `switched to remote "b"`)
	require.Equal(t, "b", loadConfig(t).CurrentRemote)

	err = execute(newRemoteUseCommand(), "nope")
	require.ErrorContains(t, err, `remote "nope" does not exist`)
}

func TestRemoteListAndBareRemote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	output, err := runCapturingStdout(t, func() error {
		return execute(newRemoteListCommand())
	})
	require.NoError(t, err)
	require.Contains(t, output, "no remotes; run `skali remote add <name> <url>`")

	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "b",
		Remotes: map[string]*cliconfig.Remote{
			"a": {Master: "https://a.example.com"},
			"b": {Master: "https://b.example.com", Token: "tok"},
		},
	})

	// Bare `skali remote` behaves exactly like `skali remote list`.
	output, err = runCapturingStdout(t, func() error {
		return execute(newRemoteCommand())
	})
	require.NoError(t, err)
	require.Contains(t, output, "  a  https://a.example.com\n")
	require.Contains(t, output, "* b  https://b.example.com  [logged in]\n")
}

func TestRemoteListHidesLocal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "b",
		Remotes: map[string]*cliconfig.Remote{
			"local": {Master: "http://127.0.0.1:8080", Token: "tok"},
			"b":     {Master: "https://b.example.com", Token: "tok"},
		},
	})

	output, err := runCapturingStdout(t, func() error {
		return execute(newRemoteListCommand())
	})
	require.NoError(t, err)
	require.Contains(t, output, "* b  https://b.example.com  [logged in]\n")
	require.NotContains(t, output, "local")

	// Only the dev-owned local remote on file still reads as no remotes.
	seedConfig(t, &cliconfig.Config{Remotes: map[string]*cliconfig.Remote{
		"local": {Master: "http://127.0.0.1:8080", Token: "tok"},
	}})
	output, err = runCapturingStdout(t, func() error {
		return execute(newRemoteListCommand())
	})
	require.NoError(t, err)
	require.Contains(t, output, "no remotes; run `skali remote add <name> <url>`")
}

func TestRemoteUseLocalRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "a",
		Remotes: map[string]*cliconfig.Remote{
			"a":     {Master: "https://a.example.com"},
			"local": {Master: "http://127.0.0.1:8080", Token: "tok"},
		},
	})

	err := execute(newRemoteUseCommand(), "local")
	require.ErrorContains(t, err, "managed by skali dev")
	require.Equal(t, "a", loadConfig(t).CurrentRemote)
}

func TestRemoteLoginLocalRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	err := execute(newRemoteLoginCommand(), "local")
	require.ErrorContains(t, err, "managed by skali dev")
}

func TestRemoteLogoutLocalRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	err := execute(newRemoteLogoutCommand(), "local")
	require.ErrorContains(t, err, "managed by skali dev")
}

func TestRemoteRemove(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// The stored token points at a dead master: the best-effort revoke
	// fails with a warning, and the removal still goes through.
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "a",
		Remotes: map[string]*cliconfig.Remote{
			"a": {Master: "https://a.example.com"},
			"b": {Master: deadURL(t), Token: "tok-b"},
		},
	})

	output, err := runCapturingStdout(t, func() error {
		return execute(newRemoteRemoveCommand(), "b")
	})
	require.NoError(t, err)
	require.Contains(t, output, `removed remote "b"`)

	cfg := loadConfig(t)
	require.Nil(t, cfg.Remotes["b"])
	require.Equal(t, "a", cfg.CurrentRemote)
}

func TestRemoteRemoveLocalRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "local",
		Remotes: map[string]*cliconfig.Remote{
			"local": {Master: "http://skali.localhost:8080", Token: "tok"},
		},
	})

	err := execute(newRemoteRemoveCommand(), "local")
	require.ErrorContains(t, err, "managed by skali dev")
	require.ErrorContains(t, err, "skali dev reset")
	require.NotNil(t, loadConfig(t).Remotes["local"])
}

func TestRemoteRemoveUnknown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	err := execute(newRemoteRemoveCommand(), "nope")
	require.ErrorContains(t, err, `remote "nope" does not exist`)
}

func TestRemoteRemoveCurrentClearsCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "a",
		Remotes: map[string]*cliconfig.Remote{
			"a": {Master: "https://a.example.com"},
			"b": {Master: "https://b.example.com"},
		},
	})

	output, err := runCapturingStdout(t, func() error {
		return execute(newRemoteRemoveCommand(), "a")
	})
	require.NoError(t, err)
	require.Contains(t, output, `removed remote "a"`)
	require.Contains(t, output, "no remote selected; run `skali remote use <name>`")
	require.Empty(t, loadConfig(t).CurrentRemote)
}

func TestRemoteStatus(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/v1/auth/session", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user":{"email":"dana@example.com","name":"Dana"},` +
			`"session":{"id":"s1","expires_at":"2026-07-29T14:02:00Z"}}`))
	})
	srv := fakeMaster(t, mux)
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "myremote",
		Remotes: map[string]*cliconfig.Remote{
			"myremote": {Master: srv.URL, Token: "tok"},
		},
	})

	output, err := runCapturingStdout(t, func() error {
		return execute(newRemoteStatusCommand())
	})
	require.NoError(t, err)
	require.Contains(t, output, "remote:  myremote\n")
	require.Contains(t, output, "master:  "+srv.URL+"\n")
	require.Contains(t, output, "health:  ok\n")
	require.Contains(t, output, "user:    dana@example.com (Dana)\n")
	require.Contains(t, output, "session: valid, expires 2026-07-")
}

func TestRemoteStatusExpiredSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/v1/auth/session", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_token","message":"session expired"}}`))
	})
	srv := fakeMaster(t, mux)
	seedConfig(t, &cliconfig.Config{
		CurrentRemote: "myremote",
		Remotes: map[string]*cliconfig.Remote{
			"myremote": {Master: srv.URL, Token: "tok"},
		},
	})

	output, err := runCapturingStdout(t, func() error {
		return execute(newRemoteStatusCommand())
	})
	require.NoError(t, err)
	require.Contains(t, output, "session: expired or revoked; run `skali remote login`")
}
