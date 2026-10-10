package reconcile

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/kubetest"
)

// patchRecorder records the path of every PATCH request a client sends.
type patchRecorder struct {
	base  http.RoundTripper
	mu    *sync.Mutex
	paths *[]string
}

func (r patchRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodPatch {
		r.mu.Lock()
		*r.paths = append(*r.paths, request.URL.Path)
		r.mu.Unlock()
	}
	return r.base.RoundTrip(request)
}

// Live: once an environment converged, a pass sends no apply. Every object
// it renders already carries its applied configuration, so each apply ends
// at its read. Only the first pass after activation may still patch: the
// objects created during the rollout carry the ownership their create
// recorded, not their applied configuration's.
func TestLiveConvergedPassSendsNoApply(t *testing.T) {
	t.Parallel()
	config := kubetest.Config(t)
	var mu sync.Mutex
	var patched []string
	config.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return patchRecorder{base: base, mu: &mu, paths: &patched}
	})
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute}, config)
	stop := f.start(t)
	run := f.deployManifest(t, isolationManifest(f.projectName, f.projectName+".test"))
	f.waitActive(t, run.RevisionID, 3*time.Minute)
	stop()

	ctx := context.Background()
	_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
	require.NoError(t, err)
	mu.Lock()
	settled := len(patched)
	mu.Unlock()
	for range 2 {
		_, err = f.kernel.reconcileEnvironment(ctx, f.environmentID)
		require.NoError(t, err)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, patched[settled:], "a converged pass must send no apply")
}
