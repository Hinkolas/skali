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
	"github.com/Hinkolas/skali/internal/store"
)

type Deps struct {
	Auth    *auth.Service
	Store   *store.Store
	DB      *pgxpool.Pool
	Cluster *cluster.Service
	// Containers may be nil at construction (partial test harnesses);
	// handlers dereference it only at request time.
	Containers *cluster.ContainerOps
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
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
			ch := &containersHandlers{st: d.Store, containers: d.Containers}
			r.Group(func(r chi.Router) {
				r.Use(RequireAdmin)

				r.Get("/users", uh.list)
				r.Get("/nodes", nh.list)
				r.Get("/nodes/{id}/metrics", nh.metrics)
				r.Get("/nodes/{id}/containers", ch.list)

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

					r.Post("/nodes/{id}/containers", ch.create)
					r.Post("/nodes/{id}/containers/{cid}/start", ch.start)
					r.Post("/nodes/{id}/containers/{cid}/stop", ch.stop)
					r.Delete("/nodes/{id}/containers/{cid}", ch.remove)
				})
			})
		})
	})

	return r
}
