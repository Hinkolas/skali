package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/reconcile"
)

// fakeStatusSource counts computations and hands out invalidation channels
// the test drives.
type fakeStatusSource struct {
	mu        sync.Mutex
	computed  int
	channels  []chan observe.Invalidation
	cancelled int
}

func (f *fakeStatusSource) Status(context.Context, uuid.UUID) (*reconcile.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.computed++
	return &reconcile.Status{}, nil
}

func (f *fakeStatusSource) SubscribeStatus(uuid.UUID) (<-chan observe.Invalidation, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	invalidations := make(chan observe.Invalidation, 16)
	f.channels = append(f.channels, invalidations)
	return invalidations, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cancelled++
	}
}

func (f *fakeStatusSource) counts() (computed, subscribed, cancelled int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.computed, len(f.channels), f.cancelled
}

func (f *fakeStatusSource) channel(i int) chan observe.Invalidation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.channels[i]
}

func receive(t *testing.T, updates <-chan statusUpdate) statusUpdate {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-time.After(5 * time.Second):
		t.Fatal("no status update")
		return statusUpdate{}
	}
}

// Every stream of an environment is sent the status computed once per
// burst of invalidations, however many streams there are; a feed that fell
// behind its invalidations subscribes again and recomputes; the feed stops
// with its last stream.
func TestStatusFeedComputesOncePerBurstForAllStreams(t *testing.T) {
	t.Parallel()
	source := &fakeStatusSource{}
	feeds := newStatusFeeds(source)
	id := uuid.New()
	var streams []<-chan statusUpdate
	var leaves []func()
	for range 3 {
		updates, leave := feeds.subscribe(id)
		streams = append(streams, updates)
		leaves = append(leaves, leave)
	}
	require.Eventually(t, func() bool { _, subscribed, _ := source.counts(); return subscribed == 1 },
		5*time.Second, 5*time.Millisecond, "one invalidation subscription for all streams")

	for range 5 {
		source.channel(0) <- observe.Invalidation{EnvironmentID: id}
	}
	for _, updates := range streams {
		update := receive(t, updates)
		require.NoError(t, update.err)
		require.NotNil(t, update.status)
	}
	computed, _, _ := source.counts()
	require.Equal(t, 1, computed, "one computation for the burst")

	close(source.channel(0))
	for _, updates := range streams {
		receive(t, updates)
	}
	computed, subscribed, _ := source.counts()
	require.Equal(t, 2, computed)
	require.Equal(t, 2, subscribed, "subscribed again after falling behind")

	for _, leave := range leaves {
		leave()
	}
	require.Eventually(t, func() bool { _, _, cancelled := source.counts(); return cancelled == 2 },
		5*time.Second, 5*time.Millisecond, "the feed stops with its last stream")
}
