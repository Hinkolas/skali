package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/registrytoken"
	"github.com/Hinkolas/skali/internal/store"
)

// fakeAuthenticator accepts exactly one session token.
type fakeAuthenticator struct {
	token string
	user  store.User
}

func (f *fakeAuthenticator) Authenticate(_ context.Context, token string) (*store.User, *store.Session, error) {
	if token != f.token {
		return nil, nil, errors.New("invalid session")
	}
	user := f.user
	return &user, &store.Session{}, nil
}

// fakePolicy grants fixed actions per project name and for the cache.
type fakePolicy struct {
	projects map[string][]string
	cache    []string
	err      error
}

func (f *fakePolicy) ProjectActions(_ context.Context, _ *store.User, name string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.projects[name], nil
}

func (f *fakePolicy) CacheActions(_ context.Context, _ *store.User) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.cache, nil
}

func newTokenHandler(t *testing.T, policy registryAuthorizer) *registryTokenHandlers {
	t.Helper()
	keyPEM, _, err := registrytoken.GenerateSigningKeypair()
	require.NoError(t, err)
	signer, err := registrytoken.LoadSigner(keyPEM)
	require.NoError(t, err)
	return &registryTokenHandlers{
		auth:       &fakeAuthenticator{token: "session-token", user: store.User{Email: "member@example.com"}},
		policy:     policy,
		signer:     signer,
		nodeSecret: "node-secret",
		now:        func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) },
	}
}

// issueToken drives one realm request and returns the recorder.
func issueToken(h *registryTokenHandlers, service string, scopes []string, username, password string) *httptest.ResponseRecorder {
	query := url.Values{}
	if service != "" {
		query.Set("service", service)
	}
	for _, scope := range scopes {
		query.Add("scope", scope)
	}
	request := httptest.NewRequest(http.MethodGet, "/token?"+query.Encode(), nil)
	if username != "" || password != "" {
		request.SetBasicAuth(username, password)
	}
	recorder := httptest.NewRecorder()
	h.issue(recorder, request)
	return recorder
}

// tokenAccess decodes the access claim of the token in a 200 response.
func tokenAccess(t *testing.T, recorder *httptest.ResponseRecorder) []registrytoken.Access {
	t.Helper()
	var response struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, int((15 * time.Minute).Seconds()), response.ExpiresIn)
	parts := strings.Split(response.Token, ".")
	require.Len(t, parts, 3)
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims struct {
		Subject string                 `json:"sub"`
		Access  []registrytoken.Access `json:"access"`
	}
	require.NoError(t, json.Unmarshal(claimsJSON, &claims))
	return claims.Access
}

func TestRegistryTokenGrants(t *testing.T) {
	projects := &fakePolicy{
		projects: map[string][]string{"demo": {"pull", "push"}, "readonly": {"pull"}},
		cache:    []string{"pull", "push"},
	}
	tests := []struct {
		name     string
		scopes   []string
		username string
		password string
		access   []registrytoken.Access
	}{
		{
			name:     "member push and pull on existing project",
			scopes:   []string{"repository:skali/demo/web:push,pull"},
			username: "member@example.com", password: "session-token",
			access: []registrytoken.Access{{Type: "repository", Name: "skali/demo/web", Actions: []string{"push", "pull"}}},
		},
		{
			name:     "unknown or invisible project earns nothing",
			scopes:   []string{"repository:skali/ghost/web:push,pull"},
			username: "member@example.com", password: "session-token",
			access: nil,
		},
		{
			name:     "read-only membership pulls but cannot push",
			scopes:   []string{"repository:skali/readonly/web:push,pull"},
			username: "member@example.com", password: "session-token",
			access: []registrytoken.Access{{Type: "repository", Name: "skali/readonly/web", Actions: []string{"pull"}}},
		},
		{
			name:     "cache repositories are shared",
			scopes:   []string{"repository:cache/docker.io/library/nginx:push,pull"},
			username: "member@example.com", password: "session-token",
			access: []registrytoken.Access{{Type: "repository", Name: "cache/docker.io/library/nginx", Actions: []string{"push", "pull"}}},
		},
		{
			name:     "repositories outside the contract earn nothing",
			scopes:   []string{"repository:admin/tools:pull", "repository:skali/demo:pull", "repository:skali/demo/web/extra:pull"},
			username: "member@example.com", password: "session-token",
			access: nil,
		},
		{
			name:     "node credential is pull only",
			scopes:   []string{"repository:skali/demo/web:push,pull"},
			username: registrytoken.NodeUser, password: "node-secret",
			access: []registrytoken.Access{{Type: "repository", Name: "skali/demo/web", Actions: []string{"pull"}}},
		},
		{
			name:     "node credential pulls any repository",
			scopes:   []string{"repository:skali/ghost/web:pull"},
			username: registrytoken.NodeUser, password: "node-secret",
			access: []registrytoken.Access{{Type: "repository", Name: "skali/ghost/web", Actions: []string{"pull"}}},
		},
		{
			name:     "multiple scopes mint multiple grants",
			scopes:   []string{"repository:skali/demo/web:pull", "repository:cache/docker.io/library/nginx:pull"},
			username: "member@example.com", password: "session-token",
			access: []registrytoken.Access{
				{Type: "repository", Name: "skali/demo/web", Actions: []string{"pull"}},
				{Type: "repository", Name: "cache/docker.io/library/nginx", Actions: []string{"pull"}},
			},
		},
		{
			name:     "malformed and non-repository scopes are ignored",
			scopes:   []string{"garbage", "registry:catalog:*", "repository:skali/demo/web:pull"},
			username: "member@example.com", password: "session-token",
			access: []registrytoken.Access{{Type: "repository", Name: "skali/demo/web", Actions: []string{"pull"}}},
		},
		{
			name:     "login probe without scope",
			scopes:   nil,
			username: "member@example.com", password: "session-token",
			access: nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newTokenHandler(t, projects)
			recorder := issueToken(handler, registrytoken.Service, test.scopes, test.username, test.password)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			access := tokenAccess(t, recorder)
			if test.access == nil {
				require.Empty(t, access)
				return
			}
			require.Equal(t, test.access, access)
		})
	}
}

func TestRegistryTokenRejections(t *testing.T) {
	projects := &fakePolicy{projects: map[string][]string{"demo": {"pull", "push"}}}
	scope := []string{"repository:skali/demo/web:pull"}
	tests := []struct {
		name     string
		service  string
		username string
		password string
	}{
		{name: "anonymous", service: registrytoken.Service},
		{name: "wrong session token", service: registrytoken.Service, username: "member@example.com", password: "wrong"},
		{name: "wrong node secret", service: registrytoken.Service, username: registrytoken.NodeUser, password: "wrong"},
		{name: "unknown service", service: "other-registry", username: "member@example.com", password: "session-token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newTokenHandler(t, projects)
			recorder := issueToken(handler, test.service, scope, test.username, test.password)
			require.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
			require.Contains(t, recorder.Body.String(), "UNAUTHORIZED")
		})
	}
}

func TestRegistryTokenNodeUserDisabled(t *testing.T) {
	handler := newTokenHandler(t, &fakePolicy{})
	handler.nodeSecret = ""
	recorder := issueToken(handler, registrytoken.Service,
		[]string{"repository:skali/demo/web:pull"}, registrytoken.NodeUser, "anything")
	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestRegistryTokenProjectLookupFailure(t *testing.T) {
	handler := newTokenHandler(t, &fakePolicy{err: errors.New("database down")})
	recorder := issueToken(handler, registrytoken.Service,
		[]string{"repository:skali/demo/web:pull"}, "member@example.com", "session-token")
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
}
