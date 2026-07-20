package observe

import (
	"sync"

	"github.com/google/uuid"
)

// Invalidation tells a subscriber that an environment's projection changed
// and must be re-read; it carries no payload by design (read the store).
type Invalidation struct {
	EnvironmentID uuid.UUID
}

// broadcaster fans invalidations out per environment. Single-daemon by
// design, same as the journal log stream: a subscriber that falls behind is
// disconnected (channel closed) and resubscribes, re-reading the store.
type broadcaster struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan Invalidation]struct{}
}

func newBroadcaster() *broadcaster {
	return &broadcaster{subs: make(map[uuid.UUID]map[chan Invalidation]struct{})}
}

func (b *broadcaster) subscribe(environmentID uuid.UUID) (chan Invalidation, func()) {
	ch := make(chan Invalidation, 64)
	b.mu.Lock()
	if b.subs[environmentID] == nil {
		b.subs[environmentID] = make(map[chan Invalidation]struct{})
	}
	b.subs[environmentID][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if set, ok := b.subs[environmentID]; ok {
				if _, subscribed := set[ch]; subscribed {
					delete(set, ch)
					close(ch)
				}
				if len(set) == 0 {
					delete(b.subs, environmentID)
				}
			}
			b.mu.Unlock()
		})
	}
	return ch, cancel
}

func (b *broadcaster) publish(event Invalidation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	set := b.subs[event.EnvironmentID]
	for ch := range set {
		select {
		case ch <- event:
		default:
			delete(set, ch)
			close(ch)
		}
	}
	if set != nil && len(set) == 0 {
		delete(b.subs, event.EnvironmentID)
	}
}
