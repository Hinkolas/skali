package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/authz"
	"github.com/Hinkolas/skali/internal/feed"
	"github.com/Hinkolas/skali/internal/journal"
)

// projectStreamHandlers serve the project change stream: one connection per
// open project page that tells the client when to re-read.
type projectStreamHandlers struct {
	feed     *feed.Projects
	journal  *journal.Service
	resolver *authz.Resolver
}

// GET /v1/projects/{id}/stream (SSE)
//
// Payload-free change signals for everything a project's pages show:
// "ready" on connect (a reconnecting client re-reads, since it may have
// missed changes), "changed" when the project, its draft, its environments
// or their settings change or a run starts or finishes in an environment
// the caller can read, and a final "deleted" once the project is gone.
// Changes are coalesced; clients re-read what they show. Mounted outside
// the request-timeout middleware.
func (h *projectStreamHandlers) stream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, codeInternal, "streaming unsupported")
		return
	}
	user := UserFrom(r.Context())

	// Subscribe before anything is read so no change slips in between.
	// Run signals arrive per environment; they are merged into changes.
	projectSignals, cancelProject := h.feed.Subscribe(id)
	defer cancelProject()
	changes := make(chan struct{}, 1)
	notify := func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
	runs := map[uuid.UUID]func(){}
	defer func() {
		for _, cancel := range runs {
			cancel()
		}
	}()
	// follow subscribes to the run signals of every environment the caller
	// can read right now, and drops the rest. It reports whether the
	// project is still there for the caller.
	follow := func(ctx context.Context) (bool, error) {
		grant, err := h.resolver.Project(ctx, user, id)
		if errors.Is(err, authz.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !grant.Visible() {
			return false, nil
		}
		for envID, cancel := range runs {
			if env, ok := grant.Environment(envID); !ok || env.Locked() {
				cancel()
				delete(runs, envID)
			}
		}
		for envID, env := range grant.Environments {
			if _, followed := runs[envID]; followed || env.Locked() {
				continue
			}
			signals, cancel := h.journal.SubscribeEnvironmentRuns(envID)
			runs[envID] = cancel
			go func() {
				for range signals {
					notify()
				}
			}()
		}
		return true, nil
	}

	resolveCtx, cancelResolve := context.WithTimeout(r.Context(), resolveTimeout)
	present, err := follow(resolveCtx)
	cancelResolve()
	if err != nil {
		writeInternalError(r.Context(), w, "resolve access", err)
		return
	}
	if !present {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(event string) {
		fmt.Fprintf(w, "event: %s\ndata: {}\n\n", event)
		flusher.Flush()
	}
	send("ready")

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case _, open := <-projectSignals:
			if !open {
				// Fell behind; the client reconnects and re-reads.
				return
			}
			notify()
		case <-changes:
			// Let a burst settle (a purge concludes its run and deletes
			// the environment back to back) into one signal.
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			select {
			case <-changes:
			default:
			}
			resolveCtx, cancelResolve := context.WithTimeout(r.Context(), resolveTimeout)
			present, err := follow(resolveCtx)
			cancelResolve()
			if err != nil {
				// The client reconnects; its "ready" re-reads.
				return
			}
			if !present {
				send("deleted")
				return
			}
			send("changed")
		}
	}
}
