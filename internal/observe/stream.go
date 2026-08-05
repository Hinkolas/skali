package observe

import (
	"github.com/google/uuid"
)

// Invalidation tells a subscriber that an environment's projection changed
// and must be re-read; it carries no payload by design (read the store).
// Fan-out happens through broadcast.Broadcaster, same contract as the
// journal log stream: a subscriber that falls behind is disconnected and
// resubscribes, re-reading the store.
type Invalidation struct {
	EnvironmentID uuid.UUID
}
