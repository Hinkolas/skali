// Package api is the HTTP layer of cmd/skalid: routing, middleware, request
// decoding, and the error envelope. Business logic stays in internal/auth
// (and later feature packages); handlers only translate between HTTP and
// those services.
package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	apispec "github.com/Hinkolas/skali/api"
	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/buildstore"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/registry"
	"github.com/Hinkolas/skali/internal/registrytoken"
	"github.com/Hinkolas/skali/internal/runtimelogs"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/valuestore"
)

type Deps struct {
	Auth      *auth.Service
	Store     *store.Store
	DB        *pgxpool.Pool
	Projects  *project.Service
	Values    *valuestore.Service
	Deploy    *deploy.Service
	Artifacts *artifactstore.Service
	Builds    *buildstore.Service
	Journal   *journal.Service
	Reconcile *reconcile.Kernel
	// Registry is the managed-registry client; a zero-host client means
	// the build and import surfaces answer registry_disabled.
	Registry *registry.Client
	// RegistryToken signs registry auth tokens; nil (the anonymous local
	// registry) leaves the token realm unregistered.
	RegistryToken *registrytoken.Signer
	// RegistryNodeSecret is the shared node pull credential the token
	// realm accepts for the skali-node user; empty rejects that user.
	RegistryNodeSecret string
	// RuntimeLogs streams live application logs; nil-clientset streams
	// answer node_unreachable.
	RuntimeLogs *runtimelogs.Streamer
	// Capabilities is the installation's declared capability set for the
	// deployment gate.
	Capabilities []string
	// Databases serves database connection projections; nil hides the
	// routes (no substrate wired).
	Databases *dbstore.Service
	// SecretReader is the sanctioned request-time Secret read behind
	// credential reveal; nil (API-only mode) disables reveal.
	SecretReader func(ctx context.Context, namespace, name string) (map[string][]byte, error)
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	// realIP must precede everything that reads RemoteAddr (rate-limit keys,
	// session metadata).
	r.Use(realIP)
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)
	// The request timeout is applied per group below, not globally: SSE
	// streams must outlive it.

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.DB.Ping(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, codeInternal, "database unreachable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	r.Get("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(apispec.OpenAPI)
	})

	// The registry token realm: the registry-domain ingress routes /token
	// here. Registered only when a signing key is configured (production);
	// the endpoint authenticates per request via Basic, never RequireAuth.
	if d.RegistryToken != nil {
		rt := &registryTokenHandlers{
			auth: d.Auth, projects: d.Store, signer: d.RegistryToken,
			nodeSecret: d.RegistryNodeSecret, now: time.Now,
		}
		r.Get("/token", rt.issue)
	}

	h := &authHandlers{auth: d.Auth}
	jh := &runsHandlers{journal: d.Journal}
	sh := &statusHandlers{reconcile: d.Reconcile}
	lh := &logsHandlers{logs: d.RuntimeLogs}
	dh := &deploymentsHandlers{
		st: d.Store, deploy: d.Deploy, artifacts: d.Artifacts, builds: d.Builds,
		journal: d.Journal, registry: d.Registry, reconcile: d.Reconcile,
		capabilities: d.Capabilities,
	}
	ch := &clientStepsHandlers{st: d.Store, journal: d.Journal, values: d.Values}
	r.Route("/v1", func(r chi.Router) {
		// Streaming: authenticated but deliberately outside the request
		// timeout, which would cut every SSE connection at 30 seconds.
		r.Group(func(r chi.Router) {
			r.Use(RequireAuth(d.Auth))

			r.Get("/steps/{id}/logs/stream", jh.streamLogs)
			r.Get("/environments/{id}/status/stream", sh.stream)
			r.Get("/environments/{id}/logs/stream", lh.stream)
		})

		// Everything else runs under the request timeout.
		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(30 * time.Second))

			// Public: everything a client can reach without a session.
			r.Post("/auth/login", h.login)
			r.Post("/auth/2fa/verify", h.verifyTwoFactor)

			// Bearer-protected. RequireAuth stays on this group only.
			r.Group(func(r chi.Router) {
				r.Use(RequireAuth(d.Auth))

				// Never behind the reauth gate: logout and session revocation are
				// defensive, /auth/reauth is the gate's escape hatch, and
				// 2fa/confirm carries its own proof (a code from the pending
				// enrollment).
				r.Post("/auth/logout", h.logout)
				r.Post("/auth/reauth", h.reauthenticate)
				r.Get("/auth/session", h.currentSession)
				r.Get("/auth/sessions", h.listSessions)
				r.Delete("/auth/sessions/{id}", h.revokeSession)
				r.Post("/auth/2fa/confirm", h.confirmTwoFactor)

				// Sensitive self-service: sudo mode.
				r.Group(func(r chi.Router) {
					r.Use(RequireFresh(d.Auth))

					r.Post("/auth/password", h.changePassword)
					r.Post("/auth/2fa/enable", h.enableTwoFactor)
					r.Post("/auth/2fa/disable", h.disableTwoFactor)
					r.Post("/auth/2fa/backup-codes", h.regenerateBackupCodes)
				})

				// Product surface: projects, environments, drafts. Members have
				// full access; only destructive deletes need sudo mode.
				ph := &projectsHandlers{projects: d.Projects}
				eh := &environmentsHandlers{projects: d.Projects, deploy: d.Deploy, journal: d.Journal}
				r.Post("/projects", ph.create)
				r.Get("/projects", ph.list)
				r.Get("/projects/{id}", ph.get)
				r.Patch("/projects/{id}", ph.update)
				r.Get("/projects/{id}/draft", ph.getDraft)
				r.Put("/projects/{id}/draft", ph.putDraft)
				r.Post("/projects/{id}/environments", eh.create)
				r.Get("/projects/{id}/environments", eh.list)
				r.Get("/environments/{id}", eh.get)

				r.Post("/projects/{id}/definitions", ph.submitDefinition)

				vh := &valuesHandlers{projects: d.Projects, values: d.Values, st: d.Store}
				r.Get("/environments/{id}/values", vh.get)
				r.Put("/environments/{id}/values", vh.put)

				rh := &revisionsHandlers{deploy: d.Deploy, journal: d.Journal}
				r.Get("/environments/{id}/revisions", rh.list)
				r.Get("/revisions/{id}", rh.get)
				r.Get("/environments/{id}/target", rh.getTarget)
				r.Put("/environments/{id}/target", rh.putTarget)

				// The deployment coordination surface: plan, the artifact
				// window, verification, and cancellation.
				r.Post("/environments/{id}/plan", dh.plan)
				r.Post("/environments/{id}/deployments", dh.open)
				r.Get("/deployments/{id}", dh.get)
				r.Post("/deployments/{id}/complete", dh.complete)
				r.Post("/deployments/{id}/fail", dh.fail)
				r.Post("/artifacts/{id}/verify", dh.verifyArtifact)
				r.Post("/builds/{id}/heartbeat", dh.heartbeatBuild)
				r.Post("/runs/{id}/cancel", dh.cancelRun)

				// Scoped client step writes (artifacts subtree only).
				r.Post("/runs/{id}/steps", ch.ensureStep)
				r.Patch("/steps/{id}", ch.setStepStatus)
				r.Post("/steps/{id}/logs", ch.appendLogs)

				// Observation projections: served from the observed store and
				// database pointers, never a request-time cluster call.
				r.Get("/environments/{id}/status", sh.get)
				r.Get("/system/observation", sh.system)

				// Database and bucket connection projections; credential
				// reveal is the one sanctioned request-time read and needs
				// sudo mode.
				if d.Databases != nil {
					dbh := &databasesHandlers{db: d.Databases, secrets: d.SecretReader}
					bh := &bucketsHandlers{db: d.Databases, secrets: d.SecretReader}
					r.Get("/environments/{id}/databases/{key}/connection", dbh.connection)
					r.Get("/environments/{id}/buckets/{key}/connection", bh.connection)
					r.Group(func(r chi.Router) {
						r.Use(RequireFresh(d.Auth))
						r.Post("/environments/{id}/databases/{key}/credentials/reveal", dbh.reveal)
						r.Post("/environments/{id}/buckets/{key}/credentials/reveal", bh.reveal)
					})
				}

				// Run journal reads; the SSE stream lives outside this group.
				r.Get("/environments/{id}/runs", jh.list)
				r.Get("/runs/{id}", jh.get)
				r.Get("/steps/{id}/logs", jh.stepLogs)
				r.Group(func(r chi.Router) {
					r.Use(RequireFresh(d.Auth))

					r.Delete("/projects/{id}", ph.delete)
					r.Delete("/environments/{id}", eh.delete)
					r.Post("/environments/{id}/teardown", eh.teardown)
				})

				// Instance management, admins only.
				uh := &usersHandlers{st: d.Store}
				r.Group(func(r chi.Router) {
					r.Use(RequireAdmin)

					r.Get("/users", uh.list)

					// Writes additionally need sudo mode. RequireAdmin sits
					// outside RequireFresh so non-admins get "forbidden", never a
					// reauth prompt that would not help them.
					r.Group(func(r chi.Router) {
						r.Use(RequireFresh(d.Auth))

						r.Post("/users", uh.create)
						r.Patch("/users/{id}", uh.update)
						r.Delete("/users/{id}", uh.delete)
						r.Post("/users/{id}/password", uh.resetPassword)
					})
				})
			})
		})
	})

	return r
}
