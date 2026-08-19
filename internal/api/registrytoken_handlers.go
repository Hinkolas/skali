package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/authz"
	"github.com/Hinkolas/skali/internal/registrytoken"
	"github.com/Hinkolas/skali/internal/store"
)

// projectFinder is the one store read the token policy needs.
type projectFinder interface {
	GetProjectByName(ctx context.Context, name string) (store.Project, error)
}

// registryAuthorizer decides the actions a session user holds: on one
// project's release repositories, and on the shared import cache. The seam
// keeps the handler testable without a database.
type registryAuthorizer interface {
	ProjectActions(ctx context.Context, user *store.User, projectName string) ([]string, error)
	CacheActions(ctx context.Context, user *store.User) ([]string, error)
}

// registryPolicy is the production authorizer: access follows project
// membership (docs/permissions.md). Release repositories skali/<project>/<app>
// are pull+push for the project's deployers and pull only for its other
// members; non-members get nothing, so the repository does not exist for
// them. The cache is pull+push for anyone who is a deployer somewhere.
type registryPolicy struct {
	resolver *authz.Resolver
	projects projectFinder
}

func (p registryPolicy) ProjectActions(ctx context.Context, user *store.User, projectName string) ([]string, error) {
	project, err := p.projects.GetProjectByName(ctx, projectName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	grant, err := p.resolver.Project(ctx, user, project.ID)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	switch {
	case !grant.Visible():
		return nil, nil
	case grant.Deployer():
		return []string{"pull", "push"}, nil
	default:
		return []string{"pull"}, nil
	}
}

func (p registryPolicy) CacheActions(ctx context.Context, user *store.User) ([]string, error) {
	deployer, err := p.resolver.DeployerAnywhere(ctx, user)
	if err != nil {
		return nil, err
	}
	if !deployer {
		return nil, nil
	}
	return []string{"pull", "push"}, nil
}

// registryTokenHandlers serves the Docker registry token protocol realm.
// The registry's 401 challenge points every client here; the handler
// authenticates Basic credentials and mints a short-lived JWT carrying
// exactly the granted subset of the requested scopes. It never sits behind
// RequireAuth: the protocol authenticates per request.
type registryTokenHandlers struct {
	auth       auth.Authenticator
	policy     registryAuthorizer
	signer     *registrytoken.Signer
	nodeSecret string
	now        func() time.Time
}

// issue answers GET <realm>?service=...&scope=repository:<name>:<actions>.
// Credentials ride Basic auth: the node user presents the shared pull
// credential from registries.yaml, everyone else presents any username
// with a skalid session token as the password. An empty scope list is a
// docker login probe: valid credentials earn an access-free token.
func (h *registryTokenHandlers) issue(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if service := query.Get("service"); service != registrytoken.Service {
		writeTokenError(w, http.StatusUnauthorized, "unknown service")
		return
	}

	username, password, ok := r.BasicAuth()
	if !ok || password == "" {
		writeTokenError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	node := false
	subject := ""
	var user *store.User
	if username == registrytoken.NodeUser {
		if h.nodeSecret == "" ||
			subtle.ConstantTimeCompare([]byte(password), []byte(h.nodeSecret)) != 1 {
			writeTokenError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		node = true
		subject = registrytoken.NodeUser
	} else {
		var err error
		user, _, err = h.auth.Authenticate(r.Context(), password)
		if err != nil {
			writeTokenError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		subject = user.Email
	}

	var access []registrytoken.Access
	for _, scope := range query["scope"] {
		repository, actions, ok := parseScope(scope)
		if !ok {
			continue
		}
		var granted []string
		if node {
			granted = intersectActions(actions, []string{"pull"})
		} else {
			allowed, err := h.userActions(r.Context(), user, repository)
			if err != nil {
				writeTokenError(w, http.StatusInternalServerError, "authorization check failed")
				return
			}
			granted = intersectActions(actions, allowed)
		}
		if len(granted) > 0 {
			access = append(access, registrytoken.Access{
				Type: "repository", Name: repository, Actions: granted,
			})
		}
	}

	now := h.now()
	token, err := h.signer.Mint(registrytoken.Service, subject, access, now, registrytoken.TokenTTL)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "token minting failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":        token,
		"access_token": token,
		"expires_in":   int(registrytoken.TokenTTL.Seconds()),
		"issued_at":    now.UTC().Format(time.RFC3339),
	})
}

// userActions maps a repository name onto the contract layout and asks the
// policy: release repositories skali/<project>/<app> follow the user's
// project access, the shared import cache follows the deployer predicate,
// everything else is out of reach.
func (h *registryTokenHandlers) userActions(ctx context.Context, user *store.User, repository string) ([]string, error) {
	parts := strings.Split(repository, "/")
	switch {
	case parts[0] == "skali" && len(parts) == 3 && parts[1] != "" && parts[2] != "":
		return h.policy.ProjectActions(ctx, user, parts[1])
	case parts[0] == "cache" && len(parts) > 1:
		return h.policy.CacheActions(ctx, user)
	}
	return nil, nil
}

// parseScope decodes one scope parameter, repository:<name>:<actions>.
// Repository names may not contain colons, but hostname-qualified forms
// do, so the name is everything between the type and the action list. Any
// non-repository or malformed scope grants nothing.
func parseScope(scope string) (repository string, actions []string, ok bool) {
	parts := strings.Split(scope, ":")
	if len(parts) < 3 {
		return "", nil, false
	}
	resourceType := parts[0]
	// The grammar allows a class suffix, for example repository(plugin).
	if index := strings.IndexByte(resourceType, '('); index >= 0 {
		resourceType = resourceType[:index]
	}
	repository = strings.Join(parts[1:len(parts)-1], ":")
	if resourceType != "repository" || repository == "" {
		return "", nil, false
	}
	for action := range strings.SplitSeq(parts[len(parts)-1], ",") {
		if action = strings.TrimSpace(action); action != "" {
			actions = append(actions, action)
		}
	}
	return repository, actions, len(actions) > 0
}

// intersectActions keeps the requested order and drops duplicates.
func intersectActions(requested, allowed []string) []string {
	var granted []string
	for _, action := range requested {
		if slices.Contains(allowed, action) && !slices.Contains(granted, action) {
			granted = append(granted, action)
		}
	}
	return granted
}

// writeTokenError answers in the Distribution error envelope; docker
// surfaces the message verbatim on login failures.
func writeTokenError(w http.ResponseWriter, status int, message string) {
	code := "UNAUTHORIZED"
	if status >= 500 {
		code = "UNAVAILABLE"
	}
	writeJSON(w, status, map[string]any{
		"errors": []map[string]string{{"code": code, "message": message}},
	})
}
