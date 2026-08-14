package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	apispec "github.com/Hinkolas/skali/api"
	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/backup"
	"github.com/Hinkolas/skali/internal/buildstore"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/module/app"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/registry"
	"github.com/Hinkolas/skali/internal/registrytoken"
	"github.com/Hinkolas/skali/internal/runtimelogs"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
	"github.com/Hinkolas/skali/internal/valuestore"
)

type testAPI struct {
	t        *testing.T
	srv      *httptest.Server
	st       *store.Store
	svc      *auth.Service
	journal  *journal.Service
	observed *observe.Fake
	// held tracks "repository@digest" content the fake managed registry
	// answers for; registryHolds seeds it.
	held *sync.Map
	// registryHost is the fake registry's address: the Host artifact
	// references carry, distinct from the push host clients push through.
	registryHost string
	// execFake scripts the exec surface per test; unscripted calls fail.
	execFake *fakeExecService
}

// testPushHost is the public push host the test registry client carries;
// push refs must name it, artifact references never.
const testPushHost = "push.example.test"

// testInstanceID is the installation identity the test daemon answers with.
const testInstanceID = "11111111-2222-4333-8444-555555555555"

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	svc, err := auth.New(st, auth.Config{Secret: strings.Repeat("s", 32)})
	require.NoError(t, err)
	values, err := valuestore.New(st, strings.Repeat("s", 32))
	require.NoError(t, err)
	backupTargets, err := backup.NewTargetStore(st, strings.Repeat("s", 32))
	require.NoError(t, err)
	artifactSvc := artifactstore.New(st)
	deploySvc := deploy.New(st, values, artifactSvc, "test")
	journalSvc := journal.NewService(st, uuid.NewString())
	registryModules := module.NewRegistry()
	require.NoError(t, registryModules.Register(app.Module{}))
	observed := observe.NewFake()
	kernel := reconcile.New(reconcile.Deps{
		Store:    st,
		Deploy:   deploySvc,
		Values:   values,
		Journal:  journalSvc,
		Registry: registryModules,
		Observed: observed.Store,
	}, reconcile.Config{})
	// Mirror production wiring: completion hands the run to the kernel and
	// leaves it running. The kernel loop is not started in API tests, so
	// tests finish runs explicitly where the worker would.
	deploySvc.SetEnqueuer(kernel)

	// A fake managed registry: the verify surface HEADs it for digests the
	// test seeded through registryHolds.
	held := &sync.Map{}
	fakeRegistry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		repo, digest, found := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v2/"), "/manifests/")
		if found {
			if _, holds := held.Load(repo + "@" + digest); holds {
				w.Header().Set("Docker-Content-Digest", digest)
				w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
				w.Header().Set("Content-Length", "2")
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(fakeRegistry.Close)
	registryURL, err := url.Parse(fakeRegistry.URL)
	require.NoError(t, err)

	// Registry auth mirrors production: a real signer serves the /token
	// realm, so the session-credential exchange is exercised against the
	// real store-backed policy.
	keyPEM, _, err := registrytoken.GenerateSigningKeypair()
	require.NoError(t, err)
	tokenSigner, err := registrytoken.LoadSigner(keyPEM)
	require.NoError(t, err)

	execFake := &fakeExecService{}

	// StripAPIPrefix wraps here exactly as in cmd/skalid, so every test
	// doubles as proof that root paths pass through the /api wrapper.
	srv := httptest.NewServer(StripAPIPrefix(NewRouter(Deps{
		Auth:               svc,
		Store:              st,
		DB:                 pool,
		Projects:           project.New(st),
		Values:             values,
		Deploy:             deploySvc,
		Artifacts:          artifactSvc,
		Builds:             buildstore.New(st),
		Journal:            journalSvc,
		Reconcile:          kernel,
		Registry:           &registry.Client{Host: registryURL.Host, PushHost: testPushHost},
		RegistryToken:      tokenSigner,
		RegistryNodeSecret: "node-secret",
		RuntimeLogs:        &runtimelogs.Streamer{Observed: observed.Store, Store: st},
		Exec:               execFake,
		Capabilities:       []string{"application", "edge", "database"},
		Databases:          dbstore.New(st),
		BackupTargets:      backupTargets,
		Backups: backup.New(backup.Deps{
			Store: st, Journal: journalSvc, Values: values,
			DB: dbstore.New(st), Deploy: deploySvc, Targets: backupTargets,
		}, backup.Config{}),
		Version:      "test",
		InstanceName: "Test Instance",
		InstanceID:   testInstanceID,
		SecretReader: func(_ context.Context, namespace, name string) (map[string][]byte, error) {
			return map[string][]byte{
				"username":   []byte("u_" + name),
				"password":   []byte("test-password-" + name),
				"access_key": []byte("AK" + name),
				"secret_key": []byte("sk-" + name),
			}, nil
		},
	})))
	t.Cleanup(srv.Close)
	return &testAPI{t: t, srv: srv, st: st, svc: svc, journal: journalSvc,
		observed: observed, held: held, registryHost: registryURL.Host,
		execFake: execFake}
}

// exchangeToken drives the registry token realm with Basic credentials and
// returns the status plus the decoded body.
func (a *testAPI) exchangeToken(scopes []string, username, password string) (int, map[string]any) {
	a.t.Helper()
	query := url.Values{"service": []string{registrytoken.Service}}
	for _, scope := range scopes {
		query.Add("scope", scope)
	}
	req, err := http.NewRequest(http.MethodGet, a.srv.URL+"/token?"+query.Encode(), nil)
	require.NoError(a.t, err)
	req.SetBasicAuth(username, password)
	res, err := a.srv.Client().Do(req)
	require.NoError(a.t, err)
	defer res.Body.Close()
	var body map[string]any
	require.NoError(a.t, json.NewDecoder(res.Body).Decode(&body))
	return res.StatusCode, body
}

// registryHolds seeds fake managed-registry content.
func (a *testAPI) registryHolds(repository, digest string) {
	a.held.Store(repository+"@"+digest, true)
}

func (a *testAPI) createUser(email, password string) {
	a.t.Helper()
	_, err := auth.CreateUser(a.t.Context(), a.st, email, "", password, auth.RoleMember)
	require.NoError(a.t, err)
}

func (a *testAPI) createAdmin(email, password string) {
	a.t.Helper()
	_, err := auth.CreateUser(a.t.Context(), a.st, email, "", password, auth.RoleAdmin)
	require.NoError(a.t, err)
}

// do sends a JSON request and decodes the JSON response body (nil for 204s).
func (a *testAPI) do(method, path, token string, body any) (int, map[string]any) {
	a.t.Helper()
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(a.t, err)
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.srv.URL+path, reqBody)
	require.NoError(a.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := a.srv.Client().Do(req)
	require.NoError(a.t, err)
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	require.NoError(a.t, err)
	if len(raw) == 0 {
		return res.StatusCode, nil
	}
	var decoded map[string]any
	require.NoError(a.t, json.Unmarshal(raw, &decoded), "body: %s", raw)
	return res.StatusCode, decoded
}

// login performs a password login and returns the session token.
func (a *testAPI) login(email, password string) string {
	a.t.Helper()
	status, body := a.do("POST", "/v1/auth/login", "", map[string]string{"email": email, "password": password})
	require.Equal(a.t, http.StatusOK, status, "body: %v", body)
	sess, ok := body["session"].(map[string]any)
	require.True(a.t, ok, "expected session in %v", body)
	return sess["token"].(string)
}

// staleAllSessions pushes every session's last reauthentication past the
// default 15m window. The service's clock seam is unexported, so tests age the
// rows instead of advancing time.
func (a *testAPI) staleAllSessions() {
	a.t.Helper()
	_, err := a.st.Pool.Exec(a.t.Context(), "UPDATE sessions SET reauthenticated_at = now() - interval '16 minutes'")
	require.NoError(a.t, err)
}

func errorCode(t *testing.T, body map[string]any) string {
	t.Helper()
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "expected error envelope in %v", body)
	require.NotEmpty(t, errObj["message"])
	return errObj["code"].(string)
}

func TestLoginAndCurrentSession(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")

	token := a.login("nick@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/auth/session", token, nil)
	require.Equal(t, http.StatusOK, status)
	user := body["user"].(map[string]any)
	require.Equal(t, "nick@example.com", user["email"])
	require.Equal(t, false, user["two_factor_enabled"])
	sess := body["session"].(map[string]any)
	require.Equal(t, true, sess["current"])
	require.NotContains(t, sess, "token", "listed sessions must not leak tokens")
}

func TestLoginFailures(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/auth/login", "", map[string]string{"email": "nick@example.com", "password": "wrong"})
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_credentials", errorCode(t, body))

	status, body = a.do("POST", "/v1/auth/login", "", map[string]string{"email": "nick@example.com"})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	// Unknown fields are rejected.
	status, body = a.do("POST", "/v1/auth/login", "", map[string]string{"email": "nick@example.com", "password": "x", "username": "nick"})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))
}

func TestUnauthenticatedRequests(t *testing.T) {
	a := newTestAPI(t)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/auth/session"},
		{"GET", "/v1/auth/sessions"},
		{"POST", "/v1/auth/logout"},
	} {
		status, body := a.do(tc.method, tc.path, "", nil)
		require.Equal(t, http.StatusUnauthorized, status, "%s %s", tc.method, tc.path)
		require.Equal(t, "invalid_token", errorCode(t, body))
	}

	status, body := a.do("GET", "/v1/auth/session", "bogus-token", nil)
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_token", errorCode(t, body))
}

func TestLogout(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	status, _ := a.do("POST", "/v1/auth/logout", token, nil)
	require.Equal(t, http.StatusNoContent, status)

	status, _ = a.do("GET", "/v1/auth/session", token, nil)
	require.Equal(t, http.StatusUnauthorized, status)
}

func TestSessionsListAndRevoke(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	a.createUser("mallory@example.com", "hunter2hunter2")

	t1 := a.login("nick@example.com", "hunter2hunter2")
	a.login("nick@example.com", "hunter2hunter2")
	mallory := a.login("mallory@example.com", "hunter2hunter2")

	status, body := a.do("GET", "/v1/auth/sessions", t1, nil)
	require.Equal(t, http.StatusOK, status)
	sessions := body["sessions"].([]any)
	require.Len(t, sessions, 2)

	// Find the non-current session id.
	var otherID string
	for _, s := range sessions {
		sm := s.(map[string]any)
		if sm["current"] == false {
			otherID = sm["id"].(string)
		}
	}
	require.NotEmpty(t, otherID)

	// Cross-user revocation is a 404.
	status, body = a.do("DELETE", "/v1/auth/sessions/"+otherID, mallory, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))

	status, _ = a.do("DELETE", "/v1/auth/sessions/"+otherID, t1, nil)
	require.Equal(t, http.StatusNoContent, status)

	// Malformed id is also a 404.
	status, _ = a.do("DELETE", "/v1/auth/sessions/not-a-uuid", t1, nil)
	require.Equal(t, http.StatusNotFound, status)

	status, body = a.do("GET", "/v1/auth/sessions", t1, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["sessions"].([]any), 1)
}

func TestChangePassword(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "old-password")
	keep := a.login("nick@example.com", "old-password")
	other := a.login("nick@example.com", "old-password")

	status, body := a.do("POST", "/v1/auth/password", keep, map[string]string{
		"current_password": "wrong", "new_password": "new-password",
	})
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_credentials", errorCode(t, body))

	status, body = a.do("POST", "/v1/auth/password", keep, map[string]string{
		"current_password": "old-password", "new_password": "short",
	})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	status, _ = a.do("POST", "/v1/auth/password", keep, map[string]string{
		"current_password": "old-password", "new_password": "new-password",
	})
	require.Equal(t, http.StatusNoContent, status)

	// The other session is gone, the requesting one survives.
	status, _ = a.do("GET", "/v1/auth/session", other, nil)
	require.Equal(t, http.StatusUnauthorized, status)
	status, _ = a.do("GET", "/v1/auth/session", keep, nil)
	require.Equal(t, http.StatusOK, status)

	a.login("nick@example.com", "new-password")
}

func TestTwoFactorFlowOverHTTP(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	status, body := a.do("POST", "/v1/auth/2fa/enable", token, nil)
	require.Equal(t, http.StatusOK, status)
	secret := body["secret"].(string)
	require.NotEmpty(t, secret)
	require.Contains(t, body["otpauth_uri"], "otpauth://totp/")
	codes := body["backup_codes"].([]any)
	require.Len(t, codes, 10)

	// Wrong confirmation code.
	status, body = a.do("POST", "/v1/auth/2fa/confirm", token, map[string]string{"code": "000000"})
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_code", errorCode(t, body))

	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	status, _ = a.do("POST", "/v1/auth/2fa/confirm", token, map[string]string{"code": code})
	require.Equal(t, http.StatusNoContent, status)

	// Re-enabling is a conflict now.
	status, body = a.do("POST", "/v1/auth/2fa/enable", token, nil)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	// Login now returns a challenge instead of a session.
	status, body = a.do("POST", "/v1/auth/login", "", map[string]string{"email": "nick@example.com", "password": "hunter2hunter2"})
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, body, "session")
	challenge := body["challenge"].(map[string]any)
	challengeToken := challenge["token"].(string)

	// The confirm code's step is burnt; verify with a backup code instead
	// (time-independent, so the test never waits on a TOTP window).
	status, body = a.do("POST", "/v1/auth/2fa/verify", "", map[string]string{
		"challenge_token": challengeToken, "code": codes[0].(string),
	})
	require.Equal(t, http.StatusOK, status)
	sess := body["session"].(map[string]any)
	twoFAToken := sess["token"].(string)
	require.Equal(t, true, sess["user"].(map[string]any)["two_factor_enabled"])

	// Challenge is single-use.
	status, body = a.do("POST", "/v1/auth/2fa/verify", "", map[string]string{
		"challenge_token": challengeToken, "code": codes[1].(string),
	})
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_token", errorCode(t, body))

	// Regenerate backup codes, then disable 2FA. The 2FA session is freshly
	// minted, so the sudo gate is transparent here.
	status, body = a.do("POST", "/v1/auth/2fa/backup-codes", twoFAToken, nil)
	require.Equal(t, http.StatusOK, status)
	fresh := body["backup_codes"].([]any)
	require.Len(t, fresh, 10)

	status, _ = a.do("POST", "/v1/auth/2fa/disable", twoFAToken, nil)
	require.Equal(t, http.StatusNoContent, status)

	// Login is password-only again.
	a.login("nick@example.com", "hunter2hunter2")
}

func TestReauthGateAndEndpoint(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	// Fresh from login, the sudo gate is transparent.
	status, _ := a.do("POST", "/v1/auth/password", token, map[string]string{
		"current_password": "hunter2hunter2", "new_password": "correct-horse",
	})
	require.Equal(t, http.StatusNoContent, status)

	a.staleAllSessions()

	status, body := a.do("POST", "/v1/auth/password", token, map[string]string{
		"current_password": "correct-horse", "new_password": "hunter2hunter2",
	})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))

	// Empty reauth request.
	status, body = a.do("POST", "/v1/auth/reauth", token, map[string]string{})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	// Wrong password does not refresh the window.
	status, body = a.do("POST", "/v1/auth/reauth", token, map[string]string{"password": "wrong"})
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_credentials", errorCode(t, body))
	status, _ = a.do("POST", "/v1/auth/password", token, map[string]string{
		"current_password": "correct-horse", "new_password": "hunter2hunter2",
	})
	require.Equal(t, http.StatusForbidden, status)

	// Correct password re-opens the gate and the original call succeeds.
	status, _ = a.do("POST", "/v1/auth/reauth", token, map[string]string{"password": "correct-horse"})
	require.Equal(t, http.StatusNoContent, status)
	status, _ = a.do("POST", "/v1/auth/password", token, map[string]string{
		"current_password": "correct-horse", "new_password": "hunter2hunter2",
	})
	require.Equal(t, http.StatusNoContent, status)
}

func TestReauthWithTwoFactor(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")

	// Enroll and confirm 2FA over HTTP.
	status, body := a.do("POST", "/v1/auth/2fa/enable", token, nil)
	require.Equal(t, http.StatusOK, status)
	secret := body["secret"].(string)
	codes := body["backup_codes"].([]any)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	status, _ = a.do("POST", "/v1/auth/2fa/confirm", token, map[string]string{"code": code})
	require.Equal(t, http.StatusNoContent, status)

	a.staleAllSessions()

	status, body = a.do("POST", "/v1/auth/2fa/backup-codes", token, nil)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))

	// 2FA users must present a code, not a password.
	status, body = a.do("POST", "/v1/auth/reauth", token, map[string]string{"password": "hunter2hunter2"})
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_code", errorCode(t, body))

	// A backup code works…
	status, _ = a.do("POST", "/v1/auth/reauth", token, map[string]string{"code": codes[0].(string)})
	require.Equal(t, http.StatusNoContent, status)
	status, _ = a.do("POST", "/v1/auth/2fa/backup-codes", token, nil)
	require.Equal(t, http.StatusOK, status)

	// …and is consumed by the reauth.
	a.staleAllSessions()
	status, body = a.do("POST", "/v1/auth/reauth", token, map[string]string{"code": codes[0].(string)})
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_code", errorCode(t, body))
}

func TestAdminWritesGated(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	a.createUser("member@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")
	member := a.login("member@example.com", "hunter2hunter2")

	a.staleAllSessions()

	// Reads stay open for a stale admin; writes need sudo mode.
	status, _ := a.do("GET", "/v1/users", admin, nil)
	require.Equal(t, http.StatusOK, status)
	status, body := a.do("POST", "/v1/users", admin, map[string]string{
		"email": "new@example.com", "password": "hunter2hunter2", "role": "member",
	})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))

	// Non-admins get forbidden, never a reauth prompt (RequireAdmin sits
	// outside RequireFresh).
	status, body = a.do("POST", "/v1/users", member, map[string]string{
		"email": "new@example.com", "password": "hunter2hunter2", "role": "member",
	})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "forbidden", errorCode(t, body))
}

func TestHealthzAndOpenAPI(t *testing.T) {
	a := newTestAPI(t)

	status, body := a.do("GET", "/healthz", "", nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ok", body["status"])

	res, err := a.srv.Client().Get(a.srv.URL + "/openapi.yaml")
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "application/yaml", res.Header.Get("Content-Type"))
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(raw, []byte("openapi: 3.1")), "spec should be OpenAPI 3.1")
}

// TestAPIPrefixStrip proves the /api alias the production edge routes to:
// every root path also answers under /api, bare /api rewrites cleanly, and
// lookalike prefixes are left alone. Root pass-through is covered by every
// other test in the package since newTestAPI wraps with StripAPIPrefix.
func TestAPIPrefixStrip(t *testing.T) {
	a := newTestAPI(t)

	status, body := a.do("GET", "/api/healthz", "", nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ok", body["status"])

	res, err := a.srv.Client().Get(a.srv.URL + "/api/openapi.yaml")
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "application/yaml", res.Header.Get("Content-Type"))

	a.createUser("prefix@example.com", "hunter2hunter2")
	token := a.login("prefix@example.com", "hunter2hunter2")
	status, body = a.do("GET", "/api/v1/auth/session", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.NotNil(t, body["user"])

	// Bare /api rewrites to / (a plain-text chi 404, not a panic), and
	// lookalike prefixes are not stripped.
	for _, path := range []string{"/api", "/apihealthz"} {
		res, err := a.srv.Client().Get(a.srv.URL + path)
		require.NoError(t, err)
		res.Body.Close()
		require.Equal(t, http.StatusNotFound, res.StatusCode, path)
	}
}

// TestSpecCoversAllRoutes walks the chi routing tree and asserts every /v1
// route appears in the embedded OpenAPI document — the cheap guard against
// spec/handler drift.
func TestSpecCoversAllRoutes(t *testing.T) {
	a := newTestAPI(t)

	spec := string(apispec.OpenAPI)
	values, err := valuestore.New(a.st, strings.Repeat("s", 32))
	require.NoError(t, err)
	backupTargets, err := backup.NewTargetStore(a.st, strings.Repeat("s", 32))
	require.NoError(t, err)
	deploySvc := deploy.New(a.st, values, artifactstore.New(a.st), "test")
	journalSvc := journal.NewService(a.st, uuid.NewString())
	router := NewRouter(Deps{
		Auth:      a.svc,
		Store:     a.st,
		DB:        a.st.Pool,
		Projects:  project.New(a.st),
		Values:    values,
		Deploy:    deploySvc,
		Artifacts: artifactstore.New(a.st),
		Builds:    buildstore.New(a.st),
		Journal:   journalSvc,
		Reconcile: reconcile.New(reconcile.Deps{
			Store: a.st, Deploy: deploySvc, Values: values, Journal: journalSvc,
			Registry: module.NewRegistry(), Observed: observe.NewFake().Store,
		}, reconcile.Config{}),
		Registry:      &registry.Client{},
		BackupTargets: backupTargets,
		Backups: backup.New(backup.Deps{
			Store: a.st, Journal: journalSvc, Values: values,
			DB: dbstore.New(a.st), Deploy: deploySvc, Targets: backupTargets,
		}, backup.Config{}),
	}).(chi.Routes)

	routes := 0
	err = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/v1/") {
			return nil
		}
		routes++
		// chi's {id} params match the spec's path templating directly.
		require.Contains(t, spec, "  "+route+":", "route %s %s missing from openapi.yaml", method, route)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 67, routes, "route count changed; update the OpenAPI spec and this number")
}
