package engine_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/engine/enginetest"
)

// startNotifier wires a notifier over the fake with warm samplers and returns
// the doorbell. The samplers are warmed synchronously so tests distinguish
// "resampled on event" from "first sample".
func startNotifier(t *testing.T, fake *enginetest.Fake) (*engine.Sampler, *engine.InventorySampler, <-chan struct{}) {
	t.Helper()
	containers := engine.NewSampler(fake)
	containers.SampleNow(t.Context())
	inventory := engine.NewInventorySampler(fake)
	inventory.SampleNow(t.Context())

	n := engine.NewNotifier(fake, containers, inventory)
	bell, cancel := n.Subscribe()
	t.Cleanup(cancel)
	go n.Run(t.Context())
	// Let Run reach its event loop before tests emit; Emit delivers only to
	// active subscriptions.
	require.Eventually(t, func() bool { return fake.Subscribed() > 0 },
		3*time.Second, 10*time.Millisecond)
	return containers, inventory, bell
}

func TestNotifierResamplesAndRings(t *testing.T) {
	fake := enginetest.New("nginx:alpine")
	containers, inventory, bell := startNotifier(t, fake)

	obs, ok := containers.Latest()
	require.True(t, ok)
	require.Empty(t, obs, "warm sampler saw no containers yet")

	fake.Add(engine.Container{Name: "web", State: "running",
		Labels: map[string]string{engine.LabelManaged: "true"}})
	fake.Emit(engine.EventContainer)

	select {
	case <-bell:
	case <-time.After(2 * time.Second):
		t.Fatal("doorbell never rang")
	}
	obs, ok = containers.Latest()
	require.True(t, ok)
	require.Len(t, obs, 1, "the ring promises post-change state")
	require.Equal(t, "web", obs[0].Name)

	// Container events must not have touched the inventory sampler's state;
	// image events refresh it.
	fake.Inv.Images = append(fake.Inv.Images, engine.Image{ID: "sha256:x", RepoTags: []string{"x:1"}})
	inv, _ := inventory.Latest()
	require.Empty(t, inv.Images)
	fake.Emit(engine.EventImage)
	select {
	case <-bell:
	case <-time.After(2 * time.Second):
		t.Fatal("doorbell never rang for the image event")
	}
	inv, ok = inventory.Latest()
	require.True(t, ok)
	require.Len(t, inv.Images, 1)
}

func TestNotifierCoalescesBursts(t *testing.T) {
	fake := enginetest.New()
	_, _, bell := startNotifier(t, fake)

	for range 20 {
		fake.Emit(engine.EventContainer)
	}
	select {
	case <-bell:
	case <-time.After(2 * time.Second):
		t.Fatal("doorbell never rang")
	}
	// The burst fell inside one debounce window: exactly one ring. A second
	// pending ring would be buffered — give it time to show up, expect none.
	select {
	case <-bell:
		t.Fatal("burst rang more than once")
	case <-time.After(600 * time.Millisecond):
	}
}

func TestNotifierResubscribesAfterStreamLoss(t *testing.T) {
	fake := enginetest.New()
	fake.EventsErr = errors.New("stream torn down")
	containers, _, bell := startNotifier(t, fake)

	// The first subscription failed; the notifier must have backed off (1s),
	// resubscribed, and still deliver events end-to-end. Reconnecting also
	// refreshes + rings on its own (the blind-window catch-up), so don't pin
	// which ring is which — just require the event's state to land and at
	// least one ring to arrive.
	fake.Add(engine.Container{Name: "late", State: "running",
		Labels: map[string]string{engine.LabelManaged: "true"}})
	fake.Emit(engine.EventContainer)
	require.Eventually(t, func() bool {
		obs, ok := containers.Latest()
		return ok && len(obs) == 1
	}, 5*time.Second, 50*time.Millisecond, "event after reconnect must resample")
	select {
	case <-bell:
	case <-time.After(time.Second):
		t.Fatal("doorbell never rang after reconnect")
	}
}

func TestNotifierUnreadBellNeverBlocksSampling(t *testing.T) {
	fake := enginetest.New()
	containers, _, bell := startNotifier(t, fake)
	_ = bell // deliberately never read

	for i := range 5 {
		fake.Add(engine.Container{Name: string(rune('a' + i)), State: "running",
			Labels: map[string]string{engine.LabelManaged: "true"}})
		fake.Emit(engine.EventContainer)
		time.Sleep(500 * time.Millisecond) // past the debounce window each time
	}
	require.Eventually(t, func() bool {
		obs, ok := containers.Latest()
		return ok && len(obs) == 5
	}, 2*time.Second, 50*time.Millisecond, "sampling must proceed with a full doorbell buffer")
}
