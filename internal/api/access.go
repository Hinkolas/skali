package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/authz"
	"github.com/Hinkolas/skali/internal/store"
)

// Authorization of the product surface. Every /v1 route is registered
// through access.route with exactly one routeClass, which both records the
// classification (the router-walk test asserts no route escapes it) and
// installs the scope middleware that loads the addressed entity, resolves
// the caller's grant, and answers 404 (unknown or not visible) or 403 (role
// too low) before the handler runs. Handlers then refine where one route
// needs more than a minimum role (plan/open, environment creation, project
// creation) and read the grant back from the context for payloads.

type scopeKind int

const (
	// scopeNone: no entity; the group middleware (RequireAuth, RequireAdmin)
	// or the handler itself is the whole check.
	scopeNone scopeKind = iota
	scopeInstanceAdmin
	scopeProject
	// scopeDeployer: project read plus the deployer predicate.
	scopeDeployer
	scopeEnvironment
	scopeRevision
	scopeDeployment
	scopeRun
	scopeStep
	// scopeArtifact and scopeBuild resolve to the owning project and need
	// the deployer predicate.
	scopeArtifact
	scopeBuild
)

// routeClass is one row of the route classification in docs/permissions.md.
type routeClass struct {
	name  string
	scope scopeKind
	min   authz.Role
}

var (
	classPublic        = routeClass{name: "public"}
	classSelf          = routeClass{name: "self"}
	classInstanceAdmin = routeClass{name: "instance-admin", scope: scopeInstanceAdmin}
	// classProjectCreate and classProjectList are handler-checked: the
	// first needs the create_projects permission, the second filters.
	classProjectCreate = routeClass{name: "project:create"}
	classProjectList   = routeClass{name: "project:list"}

	classProjectRead     = routeClass{name: "project:read", scope: scopeProject, min: authz.Read}
	classProjectMaintain = routeClass{name: "project:maintain", scope: scopeProject, min: authz.Maintain}
	classProjectAdmin    = routeClass{name: "project:admin", scope: scopeProject, min: authz.Admin}
	classDeployer        = routeClass{name: "project:deployer", scope: scopeDeployer, min: authz.Read}

	classEnvNone     = routeClass{name: "environment:none", scope: scopeEnvironment, min: authz.None}
	classEnvRead     = routeClass{name: "environment:read", scope: scopeEnvironment, min: authz.Read}
	classEnvDeploy   = routeClass{name: "environment:deploy", scope: scopeEnvironment, min: authz.Deploy}
	classEnvMaintain = routeClass{name: "environment:maintain", scope: scopeEnvironment, min: authz.Maintain}
	classEnvAdmin    = routeClass{name: "environment:admin", scope: scopeEnvironment, min: authz.Admin}

	classRevisionRead     = routeClass{name: "revision:read", scope: scopeRevision, min: authz.Read}
	classDeploymentRead   = routeClass{name: "deployment:read", scope: scopeDeployment, min: authz.Read}
	classDeploymentDeploy = routeClass{name: "deployment:deploy", scope: scopeDeployment, min: authz.Deploy}
	classRunRead          = routeClass{name: "run:read", scope: scopeRun, min: authz.Read}
	classRunDeploy        = routeClass{name: "run:deploy", scope: scopeRun, min: authz.Deploy}
	classStepRead         = routeClass{name: "step:read", scope: scopeStep, min: authz.Read}
	classStepDeploy       = routeClass{name: "step:deploy", scope: scopeStep, min: authz.Deploy}
	classArtifactDeployer = routeClass{name: "artifact:deployer", scope: scopeArtifact}
	classBuildDeployer    = routeClass{name: "build:deployer", scope: scopeBuild}
)

// access is the router's authorization layer.
type access struct {
	resolver *authz.Resolver
	st       *store.Store
	// classes records "METHOD /v1/pattern" -> class for every registered
	// route; the router-walk test reads it.
	classes map[string]routeClass
}

func newAccess(st *store.Store) *access {
	return &access{resolver: authz.New(st), st: st, classes: map[string]routeClass{}}
}

// route registers one /v1 route under its class.
func (a *access) route(r chi.Router, method, pattern string, class routeClass, h http.HandlerFunc) {
	a.classes[method+" /v1"+pattern] = class
	r.With(a.middleware(class)...).Method(method, pattern, h)
}

// root registers one route outside /v1 (health, spec, the registry realm);
// those are public by construction.
func (a *access) root(r chi.Router, method, pattern string, h http.HandlerFunc) {
	a.classes[method+" "+pattern] = classPublic
	r.Method(method, pattern, h)
}

func (a *access) middleware(class routeClass) []func(http.Handler) http.Handler {
	switch class.scope {
	case scopeInstanceAdmin:
		return []func(http.Handler) http.Handler{RequireAdmin}
	case scopeProject:
		return []func(http.Handler) http.Handler{a.requireProject(class.min, false)}
	case scopeDeployer:
		return []func(http.Handler) http.Handler{a.requireProject(authz.Read, true)}
	case scopeEnvironment:
		return []func(http.Handler) http.Handler{a.requireEnvironment(class.min)}
	case scopeRevision:
		return []func(http.Handler) http.Handler{a.requireRevision(class.min)}
	case scopeDeployment:
		return []func(http.Handler) http.Handler{a.requireDeployment(class.min)}
	case scopeRun:
		return []func(http.Handler) http.Handler{a.requireRun(class.min)}
	case scopeStep:
		return []func(http.Handler) http.Handler{a.requireStep(class.min)}
	case scopeArtifact:
		return []func(http.Handler) http.Handler{a.requireArtifact()}
	case scopeBuild:
		return []func(http.Handler) http.Handler{a.requireBuild()}
	}
	return nil
}

// Context carriage for what the scope middleware resolved.
const (
	ctxKeyGrant ctxKey = iota + 100
	ctxKeyEnvironmentGrant
	ctxKeyEnvironment
	ctxKeyRun
	ctxKeyStep
	ctxKeyDeployment
	ctxKeyRevision
)

func grantFrom(ctx context.Context) *authz.Grant {
	g, _ := ctx.Value(ctxKeyGrant).(*authz.Grant)
	return g
}

func environmentGrantFrom(ctx context.Context) *authz.EnvironmentGrant {
	g, _ := ctx.Value(ctxKeyEnvironmentGrant).(*authz.EnvironmentGrant)
	return g
}

func environmentFrom(ctx context.Context) *store.Environment {
	e, _ := ctx.Value(ctxKeyEnvironment).(*store.Environment)
	return e
}

func runFrom(ctx context.Context) *store.Run {
	r, _ := ctx.Value(ctxKeyRun).(*store.Run)
	return r
}

func stepFrom(ctx context.Context) *store.Step {
	s, _ := ctx.Value(ctxKeyStep).(*store.Step)
	return s
}

func deploymentFrom(ctx context.Context) *store.Deployment {
	d, _ := ctx.Value(ctxKeyDeployment).(*store.Deployment)
	return d
}

func revisionFrom(ctx context.Context) *store.Revision {
	r, _ := ctx.Value(ctxKeyRevision).(*store.Revision)
	return r
}

// resolveTimeout bounds the resolver reads on the streaming group, which
// has no request timeout of its own.
const resolveTimeout = 10 * time.Second

// projectGrant resolves the caller's grant on a project and answers 404 when
// the project is unknown or invisible. Returns nil after writing.
func (a *access) projectGrant(w http.ResponseWriter, r *http.Request, projectID uuid.UUID) *authz.Grant {
	ctx, cancel := context.WithTimeout(r.Context(), resolveTimeout)
	defer cancel()
	grant, err := a.resolver.Project(ctx, UserFrom(r.Context()), projectID)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return nil
		}
		writeInternalError(r.Context(), w, "resolve access", err)
		return nil
	}
	if !grant.Visible() {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return nil
	}
	return grant
}

// environmentGrant checks the caller's effective role on one environment of
// an already-resolved grant. Returns nil after writing 404 (not in the
// project) or 403 (below min).
func environmentGrant(w http.ResponseWriter, grant *authz.Grant, environmentID uuid.UUID, min authz.Role) *authz.EnvironmentGrant {
	envGrant, ok := grant.Environment(environmentID)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return nil
	}
	if !envGrant.Role.AtLeast(min) {
		writeError(w, http.StatusForbidden, codeForbidden, authz.Required(min, "environment", envGrant.Name))
		return nil
	}
	return envGrant
}

func (a *access) requireProject(min authz.Role, deployer bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			grant := a.projectGrant(w, r, id)
			if grant == nil {
				return
			}
			if !grant.ProjectRole.AtLeast(min) {
				writeError(w, http.StatusForbidden, codeForbidden, authz.Required(min, "project", grant.ProjectName))
				return
			}
			if deployer && !grant.Deployer() {
				writeError(w, http.StatusForbidden, codeForbidden,
					authz.Required(authz.Deploy, "project", grant.ProjectName)+" (on at least one environment)")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyGrant, grant)))
		})
	}
}

func (a *access) requireEnvironment(min authz.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			env, err := a.st.GetEnvironmentByID(r.Context(), id)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, codeNotFound, "not found")
					return
				}
				writeInternalError(r.Context(), w, "get environment", err)
				return
			}
			grant := a.projectGrant(w, r, env.ProjectID)
			if grant == nil {
				return
			}
			envGrant := environmentGrant(w, grant, env.ID, min)
			if envGrant == nil {
				return
			}
			next.ServeHTTP(w, r.WithContext(withEnvironment(r.Context(), grant, envGrant, &env)))
		})
	}
}

func withEnvironment(ctx context.Context, grant *authz.Grant, envGrant *authz.EnvironmentGrant, env *store.Environment) context.Context {
	ctx = context.WithValue(ctx, ctxKeyGrant, grant)
	ctx = context.WithValue(ctx, ctxKeyEnvironmentGrant, envGrant)
	if env != nil {
		ctx = context.WithValue(ctx, ctxKeyEnvironment, env)
	}
	return ctx
}

// scoped resolves an environment-owned entity: the environment row (which
// may have been deleted from under the entity) and the caller's role on it.
// Entities without an environment (runs outside one) are instance-admin
// only; everyone else sees 404.
func (a *access) scoped(w http.ResponseWriter, r *http.Request, environmentID *uuid.UUID, min authz.Role) (context.Context, bool) {
	if environmentID == nil {
		if !isInstanceAdmin(r) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return nil, false
		}
		return r.Context(), true
	}
	env, err := a.st.GetEnvironmentByID(r.Context(), *environmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return nil, false
		}
		writeInternalError(r.Context(), w, "get environment", err)
		return nil, false
	}
	grant := a.projectGrant(w, r, env.ProjectID)
	if grant == nil {
		return nil, false
	}
	envGrant := environmentGrant(w, grant, env.ID, min)
	if envGrant == nil {
		return nil, false
	}
	return withEnvironment(r.Context(), grant, envGrant, &env), true
}

func (a *access) requireRun(min authz.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			run, err := a.st.GetRunByID(r.Context(), id)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, codeNotFound, "not found")
					return
				}
				writeInternalError(r.Context(), w, "get run", err)
				return
			}
			ctx, ok := a.scoped(w, r, run.EnvironmentID, min)
			if !ok {
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, ctxKeyRun, &run)))
		})
	}
}

func (a *access) requireStep(min authz.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			step, err := a.st.GetStepByID(r.Context(), id)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, codeNotFound, "not found")
					return
				}
				writeInternalError(r.Context(), w, "get step", err)
				return
			}
			run, err := a.st.GetRunByID(r.Context(), step.RunID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, codeNotFound, "not found")
					return
				}
				writeInternalError(r.Context(), w, "get run", err)
				return
			}
			ctx, ok := a.scoped(w, r, run.EnvironmentID, min)
			if !ok {
				return
			}
			ctx = context.WithValue(ctx, ctxKeyRun, &run)
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, ctxKeyStep, &step)))
		})
	}
}

func (a *access) requireDeployment(min authz.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			deployment, err := a.st.GetDeploymentByID(r.Context(), id)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, codeNotFound, "not found")
					return
				}
				writeInternalError(r.Context(), w, "get deployment", err)
				return
			}
			ctx, ok := a.scoped(w, r, &deployment.EnvironmentID, min)
			if !ok {
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, ctxKeyDeployment, &deployment)))
		})
	}
}

func (a *access) requireRevision(min authz.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			revision, err := a.st.GetRevisionByID(r.Context(), id)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, codeNotFound, "not found")
					return
				}
				writeInternalError(r.Context(), w, "get revision", err)
				return
			}
			ctx, ok := a.scoped(w, r, &revision.EnvironmentID, min)
			if !ok {
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, ctxKeyRevision, &revision)))
		})
	}
}

// projectDeployer answers 404 for a missing owner, then the deployer check.
func (a *access) projectDeployer(w http.ResponseWriter, r *http.Request, projectID *uuid.UUID) (context.Context, bool) {
	if projectID == nil {
		if !isInstanceAdmin(r) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return nil, false
		}
		return r.Context(), true
	}
	grant := a.projectGrant(w, r, *projectID)
	if grant == nil {
		return nil, false
	}
	if !grant.Deployer() {
		writeError(w, http.StatusForbidden, codeForbidden,
			authz.Required(authz.Deploy, "project", grant.ProjectName)+" (on at least one environment)")
		return nil, false
	}
	return context.WithValue(r.Context(), ctxKeyGrant, grant), true
}

func (a *access) requireArtifact() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			artifact, err := a.st.GetArtifactByID(r.Context(), id)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, codeNotFound, "not found")
					return
				}
				writeInternalError(r.Context(), w, "get artifact", err)
				return
			}
			ctx, ok := a.projectDeployer(w, r, artifact.ProjectID)
			if !ok {
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (a *access) requireBuild() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			build, err := a.st.GetBuildByID(r.Context(), id)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, codeNotFound, "not found")
					return
				}
				writeInternalError(r.Context(), w, "get build", err)
				return
			}
			ctx, ok := a.projectDeployer(w, r, build.ProjectID)
			if !ok {
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isInstanceAdmin reads the instance role of the request's user.
func isInstanceAdmin(r *http.Request) bool {
	user := UserFrom(r.Context())
	return user != nil && user.Role == auth.RoleAdmin
}
