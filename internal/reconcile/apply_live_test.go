package reconcile

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
)

// requestRecorder records the path of every request a client sends with
// method.
type requestRecorder struct {
	base   http.RoundTripper
	method string
	mu     *sync.Mutex
	paths  *[]string
}

func (r requestRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == r.method {
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
		return requestRecorder{base: base, method: http.MethodPatch, mu: &mu, paths: &patched}
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

// Live: a converged pass under kube.WithCachedReads reads what it applies
// from the watch caches. The kinds this fixture caches are not read from
// the API server at all, while a pass without it reads every one of them.
func TestLiveConvergedPassReadsFromCache(t *testing.T) {
	t.Parallel()
	config := kubetest.Config(t)
	var mu sync.Mutex
	var reads []string
	config.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return requestRecorder{base: base, method: http.MethodGet, mu: &mu, paths: &reads}
	})
	f := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute}, config)
	f.start(t)
	run := f.deployManifest(t, isolationManifest(f.projectName, f.projectName+".test"))
	f.waitActive(t, run.RevisionID, 3*time.Minute)

	// The kernel keeps running, so its caches stay fresh; these passes
	// take turns with its own on the environment lock.
	pass := func(ctx context.Context) []string {
		mu.Lock()
		reads = nil
		mu.Unlock()
		_, err := f.kernel.reconcileEnvironment(ctx, f.environmentID)
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		var cached []string
		for _, path := range reads {
			if path == "/api/v1/namespaces/"+f.namespace ||
				strings.HasPrefix(path, "/apis/apps/v1/namespaces/"+f.namespace+"/deployments/") ||
				strings.HasPrefix(path, "/api/v1/namespaces/"+f.namespace+"/services/") ||
				strings.HasPrefix(path, "/apis/networking.k8s.io/v1/namespaces/"+f.namespace+"/networkpolicies/") {
				cached = append(cached, path)
			}
		}
		return cached
	}
	ctx := context.Background()
	require.Len(t, pass(ctx), 4, "a live pass reads the namespace, policy, Service and Deployment")
	require.Eventually(t, func() bool { return len(pass(kube.WithCachedReads(ctx))) == 0 }, 30*time.Second, 500*time.Millisecond,
		"a converged pass reads them from the caches")
}
