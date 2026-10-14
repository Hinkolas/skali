// Package feed is the project-level invalidation plane behind
// GET /v1/projects/{id}/stream: payload-free signals that something a
// project's pages show (the project itself, its draft, its environments and
// their settings) may have changed. Subscribers re-read. Run starts and
// finishes are not published here; the stream merges the journal's
// per-environment run signals for those.
package feed

import (
	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/broadcast"
)

// Projects fans project change signals out to the stream subscribers.
type Projects struct {
	b *broadcast.Broadcaster[struct{}]
}

func NewProjects() *Projects {
	return &Projects{b: broadcast.New[struct{}](16)}
}

// Subscribe signals whenever the project may have changed, until cancel is
// called or the subscriber falls behind (its channel closes).
func (p *Projects) Subscribe(projectID uuid.UUID) (<-chan struct{}, func()) {
	return p.b.Subscribe(projectID)
}

// Publish signals a project change. Call only after the change committed.
// Nil-safe: services built without a feed (tests, seed) publish nothing.
func (p *Projects) Publish(projectID uuid.UUID) {
	if p == nil {
		return
	}
	p.b.Publish(projectID, struct{}{})
}
