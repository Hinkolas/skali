package substrate

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

// TestLiveSystemClaimSameSubstrate is the R5 exit criterion behind R6: a
// synthetic system claim (shaped like object-storage/metadata) provisions
// through exactly the same claim, placement, pool, tenant, and credential
// paths as user claims, and the installer-owned skali-system namespace is
// never touched.
func TestLiveSystemClaimSameSubstrate(t *testing.T) {
	config := kubetest.Config(t)
	pool := testdb.New(t)
	ctx := context.Background()

	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	installOperator(t, client)

	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observe.NewStore(nil),
	}, Config{Managed: false})
	cleanupPlatform(t, client)

	spec := dbstore.ClaimSpec{
		Engine: "postgres", Major: 17,
		Isolation: "dedicated", Availability: "single",
	}
	outputs, err := controller.EnsureSystemClaim(ctx, "synthetic/exit-criteria", spec)
	require.NoError(t, err)
	require.False(t, outputs.Provisioned)
	require.NotEmpty(t, outputs.Waiting)

	row, err := dbSvc.LiveSystemClaim(ctx, "synthetic/exit-criteria")
	require.NoError(t, err)
	require.Equal(t, "system/synthetic/exit-criteria", row.OwnerRef)

	deadline := time.Now().Add(5 * time.Minute)
	for !outputs.Provisioned {
		require.False(t, time.Now().After(deadline),
			"system claim not provisioned; last wait: %s", outputs.Waiting)
		requeue, err := controller.reconcileClaim(ctx, row.ID)
		if err != nil {
			t.Logf("reconcile (retrying): %v", err)
		} else if fresh, freshErr := dbSvc.GetClaim(ctx, row.ID); freshErr == nil {
			stepClaim(t, "system claim", requeue, err,
				claim.Phase(fresh.Phase), controller.WaitingReason(row.ID))
		}
		outputs, err = controller.EnsureSystemClaim(ctx, "synthetic/exit-criteria", spec)
		require.NoError(t, err)
		time.Sleep(2 * time.Second)
	}

	// Same substrate, same shapes: the dev collapse lands the system claim
	// on the shared dev pool exactly like a user claim; the credential is a
	// platform Secret; there is no environment mirror anywhere.
	tenant, err := dbSvc.LiveTenant(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, "pg17-shared-rw.skali-platform.svc.cluster.local", outputs.Host)
	require.Equal(t, tenant.DatabaseName, outputs.Database)
	require.Equal(t, tenant.RoleName, outputs.Username)
	credential, err := client.Clientset.CoreV1().Secrets(Namespace).
		Get(ctx, outputs.CredentialSecret, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, credential.Data["password"])

	// The installer-owned bootstrap surface stays untouched: no substrate
	// object ever lands in skali-system.
	secrets, err := client.Clientset.CoreV1().Secrets("skali-system").
		List(ctx, metav1.ListOptions{LabelSelector: "skali.dev/claim"})
	require.NoError(t, err)
	require.Empty(t, secrets.Items)

	// System claims release through their own path.
	require.NoError(t, controller.ReleaseSystemClaim(ctx, "synthetic/exit-criteria"))
	releaseDeadline := time.Now().Add(3 * time.Minute)
	for {
		require.False(t, time.Now().After(releaseDeadline), "system claim never released")
		requeue, err := controller.reconcileClaim(ctx, row.ID)
		if err != nil {
			t.Logf("teardown (retrying): %v", err)
		}
		if _, liveErr := dbSvc.LiveSystemClaim(ctx, "synthetic/exit-criteria"); liveErr != nil {
			require.ErrorIs(t, liveErr, dbstore.ErrNotFound)
			break
		}
		if err == nil {
			if fresh, freshErr := dbSvc.GetClaim(ctx, row.ID); freshErr == nil {
				stepClaim(t, "system teardown", requeue, err,
					claim.Phase(fresh.Phase), controller.WaitingReason(row.ID))
			}
		}
		time.Sleep(2 * time.Second)
	}
}
