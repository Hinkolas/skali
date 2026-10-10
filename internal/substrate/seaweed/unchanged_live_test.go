package seaweed_test

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

type patchCounter struct {
	base    http.RoundTripper
	patches *atomic.Int64
}

func (c patchCounter) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodPatch {
		c.patches.Add(1)
	}
	return c.base.RoundTrip(request)
}

// Live: the object store's objects, applied again unchanged, send nothing.
// The server defaults what they leave unset inside fields it replaces
// whole (a claim template, a field selector, a port's target), so
// rendering anything else would differ from what the server keeps and
// send every pass's apply.
func TestLiveStoreApplySendsNothingUnchanged(t *testing.T) {
	t.Parallel()
	config := kubetest.Config(t)
	var patches atomic.Int64
	config.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return patchCounter{base: base, patches: &patches}
	})
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	clientset := kubetest.Clientset(t)
	ctx := context.Background()

	for name, render := range map[string]func(seaweed.StoreSpec) []runtime.Object{
		"production": seaweed.RenderProduction,
		"dev": func(spec seaweed.StoreSpec) []runtime.Object {
			return append(seaweed.RenderDev(spec), seaweed.RenderDevS3NodePort(spec.Namespace))
		},
	} {
		namespace := kubetest.Namespace(t, clientset)
		objects := render(seaweed.StoreSpec{Namespace: namespace, Masters: 3, Filers: 2, Replication: "001", Managed: true})
		for _, object := range objects {
			_, err := client.ApplyAs(ctx, object, kube.FieldManagerPlatform, false)
			require.NoError(t, err)
		}
		for _, object := range objects {
			before := patches.Load()
			_, err := client.ApplyAs(ctx, object, kube.FieldManagerPlatform, false)
			require.NoError(t, err)
			require.Equal(t, before, patches.Load(), "%s %s is unchanged", name, describe(object))
		}
	}
}

func describe(object runtime.Object) string {
	accessor, err := meta.Accessor(object)
	if err != nil {
		return object.GetObjectKind().GroupVersionKind().Kind
	}
	return fmt.Sprintf("%s/%s", object.GetObjectKind().GroupVersionKind().Kind, accessor.GetName())
}
