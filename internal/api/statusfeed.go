package api

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/reconcile"
)

// statusSource computes an environment's status and tells when it may
// have changed; the reconcile kernel.
type statusSource interface {
	Status(ctx context.Context, environmentID uuid.UUID) (*reconcile.Status, error)
	SubscribeStatus(environmentID uuid.UUID) (<-chan observe.Invalidation, func())
}

const (
	// statusCoalesce gathers the invalidations of one burst (a rollout
	// touches many objects at once) into one computation.
	statusCoalesce = 250 * time.Millisecond
	// statusTimeout bounds one computation, which no request bounds.
	statusTimeout = 10 * time.Second
)

// statusFeeds shares one status computation per environment among all of
// its open status streams: an invalidation burst costs one computation
// however many tabs watch the environment, and each stream is sent the
// same document.
type statusFeeds struct {
	source statusSource

	mu    sync.Mutex
	feeds map[uuid.UUID]*statusFeed
}

func newStatusFeeds(source statusSource) *statusFeeds {
	return &statusFeeds{source: source, feeds: map[uuid.UUID]*statusFeed{}}
}

type statusFeed struct {
	subscribers map[chan statusUpdate]struct{}
	done        chan struct{}
}

// statusUpdate is one computation's outcome.
type statusUpdate struct {
	status *reconcile.Status
	err    error
}

// subscribe joins environmentID's feed, starting it for the first stream.
// Every invalidation after it returns reaches the stream as an update. The
// channel holds the latest update only: a stream that falls behind skips
// to the newest document. The returned func leaves the feed, which stops
// with its last stream.
func (f *statusFeeds) subscribe(environmentID uuid.UUID) (<-chan statusUpdate, func()) {
	updates := make(chan statusUpdate, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	feed, ok := f.feeds[environmentID]
	if !ok {
		feed = &statusFeed{subscribers: map[chan statusUpdate]struct{}{}, done: make(chan struct{})}
		f.feeds[environmentID] = feed
		invalidations, cancel := f.source.SubscribeStatus(environmentID)
		go f.run(environmentID, feed, invalidations, cancel)
	}
	feed.subscribers[updates] = struct{}{}
	return updates, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(feed.subscribers, updates)
		if len(feed.subscribers) == 0 && f.feeds[environmentID] == feed {
			delete(f.feeds, environmentID)
			close(feed.done)
		}
	}
}

// run computes the status once per burst of invalidations and hands it
// to every stream of the feed, until the feed's last stream leaves.
func (f *statusFeeds) run(environmentID uuid.UUID, feed *statusFeed,
	invalidations <-chan observe.Invalidation, cancel func()) {
	defer func() { cancel() }()
	// next takes one invalidation. A closed channel means the feed fell
	// behind its invalidations: it subscribes again and counts that as
	// one, since what was missed is unknown.
	next := func(open bool) {
		if !open {
			cancel()
			invalidations, cancel = f.source.SubscribeStatus(environmentID)
		}
	}
	for {
		select {
		case <-feed.done:
			return
		case _, open := <-invalidations:
			next(open)
		}
		// The rest of the burst the invalidation began joins it.
		window := time.NewTimer(statusCoalesce)
	burst:
		for {
			select {
			case <-feed.done:
				window.Stop()
				return
			case <-window.C:
				break burst
			case _, open := <-invalidations:
				next(open)
			}
		}
		ctx, stop := context.WithTimeout(context.Background(), statusTimeout)
		status, err := f.source.Status(ctx, environmentID)
		stop()
		f.publish(feed, statusUpdate{status: status, err: err})
	}
}

func (f *statusFeeds) publish(feed *statusFeed, update statusUpdate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for updates := range feed.subscribers {
		// Only this goroutine sends, so once the stale update is taken the
		// send cannot block.
		select {
		case <-updates:
		default:
		}
		updates <- update
	}
}
