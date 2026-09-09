// Package api is the HTTP layer of cmd/skalid: routing, middleware, request
// decoding, and the error envelope. Business logic stays in internal/auth
// (and later feature packages); handlers only translate between HTTP and
// those services.
package api

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	apispec "github.com/Hinkolas/skali/api"
	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/backup"
	"github.com/Hinkolas/skali/internal/buildstore"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/metrics"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/registry"
	"github.com/Hinkolas/skali/internal/registrytoken"
	"github.com/Hinkolas/skali/internal/runtimelogs"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/updates"
	"github.com/Hinkolas/skali/internal/valuestore"
)

type Deps struct {
	// TrustProxy identifies reverse proxies allowed to supply client addresses.
	// Nil trusts no forwarded headers.
	TrustProxy func(netip.Addr) bool
	Auth       *auth.Service
	Store      *store.Store
	DB         *pgxpool.Pool
	Projects   *project.Service
	Values     *valuestore.Service
	Deploy     *deploy.Service
	Artifacts  *artifactstore.Service
	Builds     *buildstore.Service
	Journal    *journal.Service
	Reconcile  *reconcile.Kernel
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
	// Exec runs interactive commands in app pods over a WebSocket; nil
	// hides the route (tests without a fake). Production always wires
	// *podexec.Service, which answers node_unreachable itself in API-only
	// mode.
	Exec ExecService
	// Capabilities is the installation's declared capability set for the
	// deployment gate.
	Capabilities []string
	// ManagedCluster mirrors the installation mode: managed clusters reject
	// local-application intercepts and the local resolution audience, both
	// of which exist only on the local dev platform.
	ManagedCluster bool
	// StorageClass is the application volume class of the cluster (empty
	// means the default local-path class). Plan previews on a managed
	// cluster without one warn that declared volume sizes are unenforced.
	StorageClass string
	// Databases serves database connection projections; nil hides the
	// routes (no substrate wired).
	Databases *dbstore.Service
	// SecretReader is the sanctioned request-time Secret read behind
	// credential reveal; nil (API-only mode) disables reveal.
	SecretReader func(ctx context.Context, namespace, name string) (map[string][]byte, error)
	// Version is the daemon build version reported on /v1/system/meta and
	// stamped onto every response as the Skali-Version header.
	Version string
	// InstanceName is the operator-chosen installation name reported on
	// /v1/system/meta; empty leaves naming to the client.
	InstanceName string
	// InstanceID is the installation identity stamped onto every response
	// as the Skali-Instance header (and reported on /v1/system/meta) so
	// clients can detect a reinstalled cluster; empty disables the header.
	InstanceID string
	// BackupTargets manages the external S3 backup locations; nil hides the
	// backup target routes.
	BackupTargets *backup.TargetStore
	// Backups executes backup and restore operations; nil (API-only mode,
	// no cluster) hides the backup routes.
	Backups *backup.Controller
	// Metrics serves usage series from stored samples; nil hides the
	// routes (tests without a store).
	Metrics *metrics.Service
	// Updates owns platform update status, scans, and settings; nil hides
	// the routes and the meta indicator.
	Updates *updates.Service
}

// StripAPIPrefix serves the router both at the root and under /api: the
// production edge routes /api to skalid while the web console owns /, and
// in-cluster clients (the BFF, the registry token realm, health probes)
// keep root paths. Only a whole /api path segment is stripped, so lookalike
// paths like /apifoo pass through untouched.
func StripAPIPrefix(next http.Handler) http.Handler {
	stripped := http.StripPrefix("/api", next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api":
			// http.StripPrefix would leave an empty path here.
			clone := r.Clone(r.Context())
			clone.URL.Path = "/"
			clone.URL.RawPath = ""
			next.ServeHTTP(w, clone)
		case strings.HasPrefix(r.URL.Path, "/api/"):
			stripped.ServeHTTP(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func NewRouter(d Deps) http.Handler {
	r, _ := newRouter(d)
	return r
}

// newRouter builds the router and returns the access layer with it so tests
// can read the route classification back.
func newRouter(d Deps) (*chi.Mux, *access) {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	// realIP must precede everything that reads RemoteAddr (rate-limit keys,
	// session metadata).
	r.Use(realIP(d.TrustProxy))
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(platformHeaders(d.InstanceID, d.Version))
	// The request timeout is applied per group below, not globally: SSE
	// streams must outlive it.

	// Every route is registered through ac with one access class; the
	// classes drive the scope middleware and the router-walk test.
	ac := newAccess(d.Store)

	ac.root(r, "GET", "/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.DB.Ping(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, codeInternal, "database unreachable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	ac.root(r, "GET", "/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(apispec.OpenAPI)
	})

	// The registry token realm: the registry-domain ingress routes /token
	// here. Registered only when a signing key is configured (production);
	// the endpoint authenticates per request via Basic, never RequireAuth.
	if d.RegistryToken != nil {
		rt := &registryTokenHandlers{
			auth: d.Auth, policy: registryPolicy{resolver: ac.resolver, projects: d.Store},
			signer: d.RegistryToken, nodeSecret: d.RegistryNodeSecret, now: time.Now,
		}
		ac.root(r, "GET", "/token", rt.issue)
	}

	h := &authHandlers{auth: d.Auth}
	jh := &runsHandlers{journal: d.Journal}
	sh := &statusHandlers{reconcile: d.Reconcile}
	lh := &logsHandlers{logs: d.RuntimeLogs}
	dh := &deploymentsHandlers{
		st: d.Store, deploy: d.Deploy, artifacts: d.Artifacts, builds: d.Builds,
		journal: d.Journal, registry: d.Registry, reconcile: d.Reconcile,
		capabilities: d.Capabilities, managed: d.ManagedCluster,
		storageClass: d.StorageClass, auth: d.Auth,
	}
	ch := &clientStepsHandlers{st: d.Store, journal: d.Journal, values: d.Values}
	r.Route("/v1", func(r chi.Router) {
		// Streaming: authenticated but deliberately outside the request
		// timeout, which would cut every SSE connection at 30 seconds.
		r.Group(func(r chi.Router) {
			r.Use(RequireAuth(d.Auth))

			ac.route(r, "GET", "/steps/{id}/logs/stream", classStepRead, jh.streamLogs)
			ac.route(r, "GET", "/runs/{id}/stream", classRunRead, jh.streamRun)
			ac.route(r, "GET", "/environments/{id}/runs/stream", classEnvRead, jh.streamRuns)
			ac.route(r, "GET", "/environments/{id}/status/stream", classEnvRead, sh.stream)
			ac.route(r, "GET", "/environments/{id}/logs/stream", classEnvRead, lh.stream)

			// Exec sits with the streams (a session must outlive the
			// request timeout). The sudo gate lives in the handler: only a
			// promote-only environment demands a fresh session for a shell
			// (see docs/permissions.md).
			if d.Exec != nil {
				xh := newExecHandlers(d.Exec, d.Auth)
				ac.route(r, "GET", "/environments/{id}/exec", classEnvMaintain, xh.open)
			}
		})

		// Everything else runs under the request timeout.
		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(30 * time.Second))

			// Public: everything a client can reach without a session.
			ac.route(r, "POST", "/auth/login", classPublic, h.login)
			ac.route(r, "POST", "/auth/2fa/verify", classPublic, h.verifyTwoFactor)
			// Browser device authorization, CLI side: open a login request
			// and poll it. The poll answer carries the bearer token, so the
			// console's BFF never proxies these two.
			ac.route(r, "POST", "/auth/device/requests", classPublic, h.startDeviceLogin)
			ac.route(r, "POST", "/auth/device/token", classPublic, h.pollDevice)

			// Bearer-protected. RequireAuth stays on this group only.
			r.Group(func(r chi.Router) {
				r.Use(RequireAuth(d.Auth))

				// Never behind the reauth gate: logout and session revocation are
				// defensive, /auth/reauth is the gate's escape hatch, and
				// 2fa/confirm carries its own proof (a code from the pending
				// enrollment).
				ac.route(r, "POST", "/auth/logout", classSelf, h.logout)
				ac.route(r, "POST", "/auth/reauth", classSelf, h.reauthenticate)
				ac.route(r, "GET", "/auth/session", classSelf, h.currentSession)
				ac.route(r, "GET", "/auth/sessions", classSelf, h.listSessions)
				ac.route(r, "DELETE", "/auth/sessions/{id}", classSelf, h.revokeSession)
				ac.route(r, "POST", "/auth/2fa/confirm", classSelf, h.confirmTwoFactor)
				// Device authorization: a reauth request is bound to the
				// calling (stale) session, so it cannot sit behind the gate;
				// the console looks requests up and denies them freely.
				ac.route(r, "POST", "/auth/device/requests/reauth", classSelf, h.startDeviceReauth)
				ac.route(r, "GET", "/auth/device/codes/{user_code}", classSelf, h.lookupDevice)
				ac.route(r, "POST", "/auth/device/codes/{user_code}/deny", classSelf, h.denyDevice)

				// Sensitive self-service: sudo mode.
				r.Group(func(r chi.Router) {
					r.Use(RequireFresh(d.Auth))

					// Approving hands a terminal a session or a fresh sudo
					// window; the browser proves identity first (the console
					// answers the gate with its reauth checkpoint).
					ac.route(r, "POST", "/auth/device/codes/{user_code}/approve", classSelf, h.approveDevice)
					ac.route(r, "POST", "/auth/password", classSelf, h.changePassword)
					ac.route(r, "POST", "/auth/2fa/enable", classSelf, h.enableTwoFactor)
					ac.route(r, "POST", "/auth/2fa/disable", classSelf, h.disableTwoFactor)
					ac.route(r, "POST", "/auth/2fa/backup-codes", classSelf, h.regenerateBackupCodes)
				})

				// Product surface: projects, environments, drafts. Access is
				// per project and per environment (docs/permissions.md);
				// destructive deletes and access management need sudo mode.
				ph := &projectsHandlers{projects: d.Projects, reconcile: d.Reconcile, resolver: ac.resolver}
				eh := &environmentsHandlers{projects: d.Projects, deploy: d.Deploy, journal: d.Journal, reconcile: d.Reconcile}
				ah := &accessHandlers{projects: d.Projects}
				ac.route(r, "POST", "/projects", classProjectCreate, ph.create)
				ac.route(r, "GET", "/projects", classProjectList, ph.list)
				ac.route(r, "GET", "/projects/{id}", classProjectRead, ph.get)
				ac.route(r, "PATCH", "/projects/{id}", classProjectAdmin, ph.update)
				ac.route(r, "GET", "/projects/{id}/draft", classProjectRead, ph.getDraft)
				ac.route(r, "PUT", "/projects/{id}/draft", classDeployer, ph.putDraft)
				ac.route(r, "POST", "/projects/{id}/environments", classProjectMaintain, eh.create)
				ac.route(r, "GET", "/projects/{id}/environments", classProjectRead, eh.list)
				ac.route(r, "GET", "/projects/{id}/members", classProjectRead, ah.listMembers)
				ac.route(r, "GET", "/environments/{id}", classEnvNone, eh.get)
				ac.route(r, "GET", "/environments/{id}/access", classEnvRead, ah.listEnvironmentAccess)

				ac.route(r, "POST", "/projects/{id}/definitions", classDeployer, ph.submitDefinition)

				vh := &valuesHandlers{projects: d.Projects, values: d.Values, st: d.Store}
				ac.route(r, "GET", "/environments/{id}/values", classEnvRead, vh.get)
				ac.route(r, "PUT", "/environments/{id}/values", classEnvMaintain, vh.put)
				ac.route(r, "DELETE", "/environments/{id}/values/{name}", classEnvMaintain, vh.del)

				rh := &revisionsHandlers{deploy: d.Deploy, journal: d.Journal}
				ac.route(r, "GET", "/environments/{id}/revisions", classEnvRead, rh.list)
				ac.route(r, "GET", "/revisions/{id}", classRevisionRead, rh.get)
				ac.route(r, "GET", "/environments/{id}/target", classEnvRead, rh.getTarget)
				ac.route(r, "PUT", "/environments/{id}/target", classEnvDeploy, rh.putTarget)

				// The deployment coordination surface: plan, the artifact
				// window, verification, and cancellation. Plan and open
				// refine deploy to maintain when the definition changes.
				ac.route(r, "POST", "/environments/{id}/plan", classEnvDeploy, dh.plan)
				ac.route(r, "POST", "/environments/{id}/deployments", classEnvDeploy, dh.open)
				ac.route(r, "POST", "/environments/{id}/restart", classEnvDeploy, dh.restart)
				ac.route(r, "POST", "/environments/{id}/applications/{key}/restart", classEnvDeploy, dh.restart)
				ac.route(r, "GET", "/deployments/{id}", classDeploymentRead, dh.get)
				ac.route(r, "POST", "/deployments/{id}/complete", classDeploymentDeploy, dh.complete)
				ac.route(r, "POST", "/deployments/{id}/fail", classDeploymentDeploy, dh.fail)
				ac.route(r, "POST", "/artifacts/{id}/verify", classArtifactDeployer, dh.verifyArtifact)
				ac.route(r, "POST", "/builds/{id}/heartbeat", classBuildDeployer, dh.heartbeatBuild)
				ac.route(r, "POST", "/runs/{id}/cancel", classRunDeploy, dh.cancelRun)

				// Scoped client step writes (artifacts subtree only, the
				// run's own actor).
				ac.route(r, "POST", "/runs/{id}/steps", classRunDeploy, ch.ensureStep)
				ac.route(r, "PATCH", "/steps/{id}", classStepDeploy, ch.setStepStatus)
				ac.route(r, "POST", "/steps/{id}/logs", classStepDeploy, ch.appendLogs)

				// Observation projections: served from the observed store and
				// database pointers, never a request-time cluster call. The
				// instance-wide views are admin-only.
				ac.route(r, "GET", "/environments/{id}/status", classEnvRead, sh.get)
				ac.route(r, "GET", "/system/observation", classInstanceAdmin, sh.system)
				ac.route(r, "GET", "/nodes", classInstanceAdmin, sh.nodes)

				// Usage series from stored samples; database reads only.
				if d.Metrics != nil {
					mrh := &metricsHandlers{metrics: d.Metrics}
					ac.route(r, "GET", "/environments/{id}/metrics", classEnvRead, mrh.environment)
					ac.route(r, "GET", "/nodes/metrics", classInstanceAdmin, mrh.nodes)
					// Current storage pictures, same database-only reads.
					ac.route(r, "GET", "/nodes/storage", classInstanceAdmin, mrh.nodesStorage)
					ac.route(r, "GET", "/projects/{id}/storage", classProjectRead, mrh.projectStorage)
				}

				// Instance facts: version, name, and identity.
				mh := &systemHandlers{
					version: d.Version, instanceName: d.InstanceName, instanceID: d.InstanceID,
					updates: d.Updates,
				}
				ac.route(r, "GET", "/system/meta", classSelf, mh.meta)

				// Database and bucket connection projections; credential
				// reveal is the one sanctioned request-time read and needs
				// sudo mode.
				if d.Databases != nil {
					dbh := &databasesHandlers{db: d.Databases, secrets: d.SecretReader}
					bh := &bucketsHandlers{db: d.Databases, secrets: d.SecretReader}
					ac.route(r, "GET", "/environments/{id}/databases/{key}/connection", classEnvRead, dbh.connection)
					ac.route(r, "GET", "/environments/{id}/buckets/{key}/connection", classEnvRead, bh.connection)
					r.Group(func(r chi.Router) {
						r.Use(RequireFresh(d.Auth))
						ac.route(r, "POST", "/environments/{id}/databases/{key}/credentials/reveal", classEnvMaintain, dbh.reveal)
						ac.route(r, "POST", "/environments/{id}/buckets/{key}/credentials/reveal", classEnvMaintain, bh.reveal)
					})
				}

				// Environment snapshots: create is 202-async like teardown,
				// listing reads the S3 manifests.
				if d.Backups != nil {
					bkh := &backupsHandlers{backups: d.Backups, st: d.Store}
					ac.route(r, "POST", "/environments/{id}/backups", classEnvDeploy, bkh.create)
					ac.route(r, "GET", "/environments/{id}/backups", classEnvRead, bkh.list)
					ac.route(r, "GET", "/projects/{id}/backups", classProjectRead, bkh.listProject)
				}

				// Run journal reads; the SSE stream lives outside this group.
				ac.route(r, "GET", "/environments/{id}/runs", classEnvRead, jh.list)
				ac.route(r, "GET", "/runs/{id}", classRunRead, jh.get)
				ac.route(r, "GET", "/steps/{id}/logs", classStepRead, jh.stepLogs)
				r.Group(func(r chi.Router) {
					r.Use(RequireFresh(d.Auth))

					// One application's fully resolved environment (skali
					// dev host runs); returns secret values, so it shares
					// reveal's sudo gate. Registered unconditionally and
					// answering 503 in API-only mode so the route set stays
					// deployment-independent.
					aeh := &appEnvHandlers{
						deploy: d.Deploy, values: d.Values, db: d.Databases,
						secrets: d.SecretReader, managed: d.ManagedCluster,
					}
					ac.route(r, "GET", "/environments/{id}/applications/{key}/environment", classEnvMaintain, aeh.resolved)

					ac.route(r, "DELETE", "/projects/{id}", classProjectAdmin, ph.delete)
					ac.route(r, "DELETE", "/environments/{id}", classEnvAdmin, eh.delete)
					ac.route(r, "POST", "/environments/{id}/teardown", classEnvAdmin, eh.teardown)
					ac.route(r, "PATCH", "/environments/{id}", classEnvAdmin, eh.update)

					// Access management: members of a project, cells of an
					// environment.
					ac.route(r, "PUT", "/projects/{id}/members/{user}", classProjectAdmin, ah.putMember)
					ac.route(r, "DELETE", "/projects/{id}/members/{user}", classProjectAdmin, ah.deleteMember)
					ac.route(r, "PUT", "/environments/{id}/access/{user}", classEnvAdmin, ah.putEnvironmentAccess)
					ac.route(r, "DELETE", "/environments/{id}/access/{user}", classEnvAdmin, ah.deleteEnvironmentAccess)

					// Restore replaces the environment's data; like
					// teardown it needs sudo mode.
					if d.Backups != nil {
						bkh := &backupsHandlers{backups: d.Backups, st: d.Store}
						ac.route(r, "POST", "/environments/{id}/restore", classEnvMaintain, bkh.restore)
					}
				})

				// The user directory: any authenticated user may read it (it
				// backs the console's add-member picker); the handler trims
				// the payload to id, email, name, and role for non-admins.
				uh := &usersHandlers{st: d.Store}
				ac.route(r, "GET", "/users", classSelf, uh.list)

				// Instance management, admins only.
				r.Group(func(r chi.Router) {
					r.Use(RequireAdmin)

					// Platform updates: the status document and a manual
					// scan are plain admin reads; every change is below.
					var uph *updatesHandlers
					if d.Updates != nil {
						uph = &updatesHandlers{updates: d.Updates}
						ac.route(r, "GET", "/system/updates", classInstanceAdmin, uph.get)
						ac.route(r, "POST", "/system/updates/scan", classInstanceAdmin, uph.scan)
					}

					// Writes additionally need sudo mode. RequireAdmin sits
					// outside RequireFresh so non-admins get "forbidden", never a
					// reauth prompt that would not help them.
					r.Group(func(r chi.Router) {
						r.Use(RequireFresh(d.Auth))

						ac.route(r, "POST", "/users", classInstanceAdmin, uh.create)
						ac.route(r, "PATCH", "/users/{id}", classInstanceAdmin, uh.update)
						ac.route(r, "DELETE", "/users/{id}", classInstanceAdmin, uh.delete)
						ac.route(r, "POST", "/users/{id}/password", classInstanceAdmin, uh.resetPassword)

						// The backup target holds external S3 credentials;
						// reads and writes both stay behind sudo mode.
						if d.BackupTargets != nil {
							bth := &backupTargetHandlers{targets: d.BackupTargets}
							ac.route(r, "GET", "/system/backup-target", classInstanceAdmin, bth.get)
							ac.route(r, "PUT", "/system/backup-target", classInstanceAdmin, bth.put)
							ac.route(r, "DELETE", "/system/backup-target", classInstanceAdmin, bth.delete)
						}

						// Starting or resuming an update rolls every node
						// and the control plane; channel and auto-update
						// decide what future scans may start on their own.
						if uph != nil {
							ac.route(r, "POST", "/system/updates/apply", classInstanceAdmin, uph.apply)
							ac.route(r, "POST", "/system/updates/resume", classInstanceAdmin, uph.resume)
							ac.route(r, "PUT", "/system/updates/settings", classInstanceAdmin, uph.putSettings)
						}
					})
				})
			})
		})
	})

	return r, ac
}
