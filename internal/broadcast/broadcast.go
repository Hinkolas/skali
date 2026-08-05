// Package broadcast provides the in-process fan-out behind live event
// feeds: subscribers register under a key, publishers deliver to every
// subscriber of that key. Single-daemon by design; a multi-replica
// control plane would replace it with a shared channel.
package broadcast

import (
	"sync"

	"github.com/google/uuid"
)

// Broadcaster fans events out to the subscribers of one uuid key.
type Broadcaster[T any] struct {
	mu     sync.Mutex
	buffer int
	subs   map[uuid.UUID]map[chan T]struct{}
}

// New returns a broadcaster whose subscriber channels buffer up to
// buffer events before the slow subscriber is disconnected.
func New[T any](buffer int) *Broadcaster[T] {
	return &Broadcaster[T]{buffer: buffer, subs: make(map[uuid.UUID]map[chan T]struct{})}
}

// Subscribe registers for the key's events until cancel is called or the
// subscriber is disconnected. The cancel function is idempotent and safe
// to call after a disconnect.
func (b *Broadcaster[T]) Subscribe(key uuid.UUID) (<-chan T, func()) {
	ch := make(chan T, b.buffer)
	b.mu.Lock()
	if b.subs[key] == nil {
		b.subs[key] = make(map[chan T]struct{})
	}
	b.subs[key][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if set, ok := b.subs[key]; ok {
				if _, subscribed := set[ch]; subscribed {
					delete(set, ch)
					close(ch)
				}
				if len(set) == 0 {
					delete(b.subs, key)
				}
			}
			b.mu.Unlock()
		})
	}
	return ch, cancel
}

// Active reports whether any subscriber exists at all: the fast path
// that keeps subscriber-only work off the no-subscriber hot path.
func (b *Broadcaster[T]) Active() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs) > 0
}

// Publish delivers to every subscriber of the key. A subscriber whose
// buffer is full is disconnected (its channel closed) rather than
// blocked or silently skipped; it resubscribes and catches up from its
// cursor or by re-reading the store.
func (b *Broadcaster[T]) Publish(key uuid.UUID, event T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	set := b.subs[key]
	for ch := range set {
		select {
		case ch <- event:
		default:
			delete(set, ch)
			close(ch)
		}
	}
	if set != nil && len(set) == 0 {
		delete(b.subs, key)
	}
}
