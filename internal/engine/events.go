package engine

import (
	"context"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

// EventKind is the coarse category of an engine change event. It says which
// observed surface is stale (containers vs the image/volume inventory),
// nothing more — consumers resample rather than interpret.
type EventKind string

const (
	EventContainer EventKind = "container"
	EventImage     EventKind = "image"
	EventVolume    EventKind = "volume"
)

// Events streams change notifications from the daemon, filtered server-side
// to the three kinds skali observes. Unmanaged workloads are deliberately
// included: image and volume events carry no ownership label, and the
// unfiltered inventory wants to hear about them anyway.
func (d *Docker) Events(ctx context.Context) (<-chan EventKind, <-chan error) {
	res := d.cli.Events(ctx, client.EventsListOptions{
		Filters: client.Filters{}.Add("type",
			string(events.ContainerEventType),
			string(events.ImageEventType),
			string(events.VolumeEventType),
		),
	})
	out := make(chan EventKind)
	errs := make(chan error, 1)
	go func() {
		defer close(errs)
		for {
			select {
			case msg := <-res.Messages:
				select {
				case out <- EventKind(msg.Type):
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				}
			case err := <-res.Err:
				errs <- classify(err)
				return
			}
		}
	}()
	return out, errs
}
