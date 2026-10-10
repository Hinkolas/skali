package deploy

import (
	"time"

	"github.com/Hinkolas/skali/internal/store"
)

// ErrEnvironmentBusy: a reconcile pass or another change held the
// environment lock for the caller's whole wait.
var ErrEnvironmentBusy = store.ErrEnvironmentBusy

// How long a change waits for the environment lock while a reconcile pass
// or another change holds it. A request answers within the client's 15 s
// timeout; a promotion runs after its request returned and waits longer
// before it fails the deployment.
const (
	requestLockWait   = 10 * time.Second
	promotionLockWait = time.Minute
)
