// Package api is the HTTP layer of cmd/skalid: routing, middleware, request
// decoding, and the error envelope. Business logic stays in internal/auth
// (and later feature packages); handlers only translate between HTTP and
// those services.
package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	apispec "github.com/Hinkolas/skali/api"
	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/cluster"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/store"
)

type Deps struct {
	Auth    *auth.Service
	Store   *store.Store
	DB      *pgxpool.Pool
	Cluster *cluster.Service
	// Registry is nil when the master runs no image mirror (CLUSTER_ADDR
	// unset); its routes then answer 503 registry_disabled.
	Registry RegistryOps
	// Workloads shares the registry gate (unified image handling needs the
	// mirror): nil disables the write routes the same way, reads stay
	// store-only.
	Workloads *reconcile.Service
	// Leader reports whether this master holds the cluster leader lease;
	// nil (tests, single-purpose harnesses) reads as false.
	Leader func() bool
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	// realIP must precede everything that reads RemoteAddr (rate-limit keys,
	// session metadata).
	r.Use(realIP)
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.DB.Ping(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, codeInternal, "database unreachable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"leader": d.Leader != nil && d.Leader(),
		})
	})

	r.Get("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(apispec.OpenAPI)
	})

	h := &authHandlers{auth: d.Auth}
	r.Route("/v1", func(r chi.Router) {
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

			// Instance management, admins only.
			uh := &usersHandlers{st: d.Store}
			nh := &nodesHandlers{st: d.Store, cluster: d.Cluster}
			ch := &containersHandlers{st: d.Store}
			rh := &registryHandlers{registry: d.Registry, st: d.Store}
			oh := &operationsHandlers{st: d.Store}
			wh := &workloadsHandlers{st: d.Store, workloads: d.Workloads}
			r.Group(func(r chi.Router) {
				r.Use(RequireAdmin)

				r.Get("/users", uh.list)
				r.Get("/nodes", nh.list)
				r.Get("/nodes/{id}/metrics", nh.metrics)

				// The cluster-wide engine surface: reads are cross-node with
				// an optional ?node= filter; writes stay node-scoped below.
				r.Get("/containers", ch.list)
				r.Get("/images", ch.images)
				r.Get("/volumes", ch.volumes)

				// The mirror catalog: what the cluster registry serves.
				r.Get("/registry/images", rh.list)

				// Task-shaped background work: poll here after a 202.
				r.Get("/operations", oh.list)
				r.Get("/operations/{id}", oh.get)

				// Desired state: the reconciler converges what these declare.
				r.Get("/workloads", wh.list)
				r.Get("/workloads/{id}", wh.get)

				// Writes additionally need sudo mode. RequireAdmin sits
				// outside RequireFresh so non-admins get "forbidden", never a
				// reauth prompt that would not help them.
				r.Group(func(r chi.Router) {
					r.Use(RequireFresh(d.Auth))

					r.Post("/users", uh.create)
					r.Patch("/users/{id}", uh.update)
					r.Delete("/users/{id}", uh.delete)
					r.Post("/users/{id}/password", uh.resetPassword)

					r.Post("/nodes/tokens", nh.createToken)
					r.Patch("/nodes/{id}", nh.update)
					r.Delete("/nodes/{id}", nh.delete)

					r.Post("/registry/images", rh.importImage)
					r.Delete("/registry/images/{id}", rh.deleteImage)

					r.Post("/workloads", wh.create)
					r.Patch("/workloads/{id}", wh.update)
					r.Delete("/workloads/{id}", wh.delete)
				})
			})
		})
	})

	return r
}
