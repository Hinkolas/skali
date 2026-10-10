package cnpg_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
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

// Live: a pool's Cluster and its Services, applied again unchanged, send
// nothing. The server defaults what they leave unset, a role's connection
// limit and a port's target, so rendering anything else would differ from
// what the server keeps and send every pass's apply.
func TestLivePoolApplySendsNothingUnchanged(t *testing.T) {
	t.Parallel()
	config := kubetest.Config(t)
	var patches atomic.Int64
	config.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return patchCounter{base: base, patches: &patches}
	})
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	namespace := kubetest.Namespace(t, kubetest.Clientset(t))
	ctx := context.Background()

	cluster := cnpg.RenderCluster(cnpg.ClusterSpec{
		Namespace: namespace, Name: "pool", Image: "ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie",
		Instances: 1, StorageBytes: 1 << 30,
		Roles: []cnpg.Role{
			{Name: "u_data_abcd1234", DisablePassword: true},
			{Name: "u_data_abcd1234_v2", SecretName: "dbcred-abcd1234-v2", Login: true, InRoles: []string{"u_data_abcd1234"}},
		},
		Parameters: map[string]string{"max_connections": "100"},
	})
	for _, object := range []runtime.Object{
		cluster,
		cnpg.RenderMetricsService(namespace, "pool"),
		cnpg.RenderPrimaryNodePortService(namespace, "pool", 0),
	} {
		_, err := client.ApplyAs(ctx, object, kube.FieldManagerPlatform, false)
		require.NoError(t, err)
		before := patches.Load()
		_, err = client.ApplyAs(ctx, object, kube.FieldManagerPlatform, false)
		require.NoError(t, err)
		require.Equal(t, before, patches.Load(), "%s is unchanged", object.GetObjectKind().GroupVersionKind().Kind)
	}
}
