package substrate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Until the object-store pass creates the platform keypair (a store still
// converging, an upgrade from a release without it), callers get a reason
// they can act on instead of the raw "secret not found".
func TestPlatformIdentityPendingUntilSecretExists(t *testing.T) {
	fake := &fakeCluster{}
	c := &Controller{deps: Deps{Cluster: fake}}

	err := c.PlatformIdentityReady(context.Background())
	require.ErrorIs(t, err, ErrPlatformIdentityPending)

	fake.secrets = map[string]*corev1.Secret{Namespace + "/" + PlatformCredentialSecret: {
		ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: PlatformCredentialSecret},
		Data:       map[string][]byte{"access_key": []byte("ak"), "secret_key": []byte("sk")},
	}}
	require.NoError(t, c.PlatformIdentityReady(context.Background()))
	accessKey, secretKey, err := c.platformCredentials(context.Background())
	require.NoError(t, err)
	require.Equal(t, "ak", accessKey)
	require.Equal(t, "sk", secretKey)
}
