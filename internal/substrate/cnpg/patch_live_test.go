package cnpg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// Live: a pool moved along a catalog patch transition rolls in place on
// the real operator. It reads as unsettled from the apply until its member
// runs the new image (cnpg.Settled, the predicate the substrate's patch
// rollout waits on), its data survives, and the server reports the new
// version.
func TestLivePoolPatchUpdate(t *testing.T) {
	t.Parallel()
	config := kubetest.Config(t)
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	namespace := kubetest.Namespace(t, kubetest.Clientset(t))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	image, ok := cnpg.Lookup("postgres", 17)
	require.True(t, ok)
	require.NotEmpty(t, image.PatchesFrom)
	from := image.PatchesFrom[0]
	render := func(ref string) *unstructured.Unstructured {
		return cnpg.RenderCluster(cnpg.ClusterSpec{
			Namespace: namespace, Name: "pool", Image: ref, Instances: 1, StorageBytes: 1 << 30,
		})
	}
	settled := func(ref string) (bool, string) {
		object, err := client.Dynamic.Resource(cnpg.ClusterGVR).Namespace(namespace).Get(ctx, "pool", metav1.GetOptions{})
		require.NoError(t, err)
		phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
		pods, err := client.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: cnpg.InstanceSelector("pool"),
		})
		require.NoError(t, err)
		return cnpg.Settled(phase, cnpg.InstancesFromPods(pods.Items), ref)
	}
	waitSettled := func(ref string) []string {
		t.Helper()
		var reasons []string
		for {
			ok, reason := settled(ref)
			if ok {
				return reasons
			}
			if len(reasons) == 0 || reasons[len(reasons)-1] != reason {
				reasons = append(reasons, reason)
			}
			select {
			case <-ctx.Done():
				t.Fatalf("pool did not settle on %s; observed %q", ref, reasons)
			case <-time.After(time.Second):
			}
		}
	}
	query := func(sql string) string {
		t.Helper()
		out, err := client.ExecInPod(ctx, namespace, cnpg.InstanceSelector("pool"), cnpg.PostgresContainer,
			cnpg.PSQLQuery("postgres", sql))
		require.NoError(t, err)
		return strings.TrimSpace(out)
	}

	_, err = client.ApplyAs(ctx, render(from), kube.FieldManagerPlatform, false)
	require.NoError(t, err)
	waitSettled(from)
	query("CREATE TABLE kept (note text); INSERT INTO kept VALUES ('before the patch')")
	fromVersion := query("SHOW server_version")

	_, err = client.ApplyAs(ctx, render(image.Ref), kube.FieldManagerPlatform, false)
	require.NoError(t, err)
	ok, _ = settled(image.Ref)
	require.False(t, ok, "the pool reads as unsettled right after the apply")
	reasons := waitSettled(image.Ref)
	t.Logf("rollout observed: %q", reasons)

	require.Equal(t, "before the patch", query("SELECT note FROM kept"))
	toVersion := query("SHOW server_version")
	require.NotEqual(t, fromVersion, toVersion)
	_, tag, _ := strings.Cut(image.Ref, ":")
	version, _, _ := strings.Cut(tag, "-")
	require.True(t, strings.HasPrefix(toVersion, version), "server reports %s, catalog %s", toVersion, version)
}
