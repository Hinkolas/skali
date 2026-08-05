package broadcast

import (
	"testing"

	"github.com/google/uuid"
)

func TestFanOutAndKeyIsolation(t *testing.T) {
	b := New[string](4)
	key, other := uuid.New(), uuid.New()
	first, cancelFirst := b.Subscribe(key)
	second, cancelSecond := b.Subscribe(key)
	defer cancelFirst()
	defer cancelSecond()
	elsewhere, cancelElsewhere := b.Subscribe(other)
	defer cancelElsewhere()

	b.Publish(key, "hello")
	if got := <-first; got != "hello" {
		t.Errorf("first subscriber got %q", got)
	}
	if got := <-second; got != "hello" {
		t.Errorf("second subscriber got %q", got)
	}
	select {
	case event := <-elsewhere:
		t.Errorf("other key received %q", event)
	default:
	}
}

func TestSlowSubscriberIsDisconnected(t *testing.T) {
	b := New[int](2)
	key := uuid.New()
	ch, cancel := b.Subscribe(key)
	defer cancel()

	for i := range 3 {
		b.Publish(key, i)
	}
	if got := <-ch; got != 0 {
		t.Errorf("first buffered event = %d", got)
	}
	if got := <-ch; got != 1 {
		t.Errorf("second buffered event = %d", got)
	}
	if _, open := <-ch; open {
		t.Error("overflowing subscriber must be disconnected (channel closed)")
	}
}

func TestCancelIsIdempotentAfterDisconnect(t *testing.T) {
	b := New[int](1)
	key := uuid.New()
	_, cancel := b.Subscribe(key)
	b.Publish(key, 1)
	b.Publish(key, 2) // overflow disconnects and closes the channel
	cancel()          // must not close twice or panic
	cancel()
	b.Publish(key, 3) // no subscribers left; must not panic
}
