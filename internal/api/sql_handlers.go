package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	kexec "k8s.io/client-go/util/exec"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/podexec"
	"github.com/Hinkolas/skali/internal/sqlsession"
)

// SQLService is the resolve-and-run surface the SQL handler needs:
// *sqlsession.Service in production, a fake in tests.
type SQLService interface {
	Resolve(ctx context.Context, environmentID uuid.UUID, opts sqlsession.Options) (*sqlsession.Target, error)
	// Run starts the client and streams the session; it calls ready once
	// the client process reads stdin.
	Run(ctx context.Context, target *sqlsession.Target, streams podexec.Streams, ready func()) (string, error)
}

// sqlHandlers serves SQL sessions against one database service: the same
// execproto WebSocket as exec, into a short-lived database client instead
// of an application container.
type sqlHandlers struct {
	sql      SQLService
	auth     *auth.Service
	upgrader websocket.Upgrader
}

func newSQLHandlers(sql SQLService, auth *auth.Service) *sqlHandlers {
	return &sqlHandlers{sql: sql, auth: auth, upgrader: sessionUpgrader()}
}

// GET /v1/environments/{id}/databases/{key}/sql?format=json&read_only=true
// Mounted with exec, outside the request timeout. The request carries only
// the session's shape; the SQL itself arrives on stdin after the upgrade,
// so it never appears in URLs, access logs, or the audit line below.
// Everything validates and resolves before the upgrade; a client that then
// fails to start ends the session with a start_failed error frame.
func (h *sqlHandlers) open(w http.ResponseWriter, r *http.Request) {
	if cookieAuthFrom(r) && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, codeForbidden, "websocket requires a same-origin Origin")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	opts := sqlsession.Options{
		Database: chi.URLParam(r, "key"),
		Format:   sqlsession.Format(query.Get("format")),
	}
	for name, target := range map[string]*bool{"tty": &opts.TTY, "read_only": &opts.ReadOnly} {
		if raw := query.Get(name); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, codeBadRequest, "invalid "+name+" value")
				return
			}
			*target = parsed
		}
	}
	if raw := query.Get("statement_timeout"); raw != "" {
		ms, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || ms < 0 {
			writeError(w, http.StatusBadRequest, codeBadRequest, "statement_timeout must be a number of milliseconds")
			return
		}
		opts.StatementTimeout = time.Duration(ms) * time.Millisecond
	}

	// SQL can change what the deploy policy protects just like a shell.
	if !requireFreshWhenProtected(w, r, h.auth) {
		return
	}

	target, err := h.sql.Resolve(r.Context(), id, opts)
	if err != nil {
		h.writeResolveError(r.Context(), w, opts.Database, err)
		return
	}

	conn, ok := upgradeSession(w, r, &h.upgrader)
	if !ok {
		return
	}
	defer conn.Close()

	start := time.Now()
	var pod string
	streamErr := bridgeSession(r.Context(), conn, target.TTY, func(ctx context.Context, streams podexec.Streams, ready func()) error {
		var err error
		pod, err = h.sql.Run(ctx, target, streams, ready)
		return err
	})

	code := 0
	if exit, ok := errors.AsType[kexec.CodeExitError](streamErr); ok {
		code = exit.Code
		streamErr = nil
	}
	user := UserFrom(r.Context())
	slog.InfoContext(r.Context(), "sql session",
		"user", user.Email,
		"user_id", user.ID,
		"environment", id,
		"database", target.Database,
		"format", target.Format,
		"tty", target.TTY,
		"read_only", target.ReadOnly,
		"pod", pod,
		"duration_ms", time.Since(start).Milliseconds(),
		"exit_code", code,
		"err", streamErr,
	)
}

func (h *sqlHandlers) writeResolveError(ctx context.Context, w http.ResponseWriter, key string, err error) {
	switch {
	case errors.Is(err, sqlsession.ErrNoCluster):
		writeError(w, http.StatusServiceUnavailable, codeNodeUnreachable,
			"no cluster is connected; SQL sessions are unavailable")
	case errors.Is(err, sqlsession.ErrEnvironmentNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, sqlsession.ErrDatabaseNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "no database "+strconv.Quote(key)+" in this environment")
	case errors.Is(err, sqlsession.ErrNotProvisioned):
		writeError(w, http.StatusConflict, codeConflict, "the database is not provisioned yet")
	case errors.Is(err, sqlsession.ErrInvalidOptions):
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
	default:
		writeInternalError(ctx, w, "sql resolve", err)
	}
}
