package installer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/kube"
)

func recordConfigMap(ownership, cluster string) *corev1.ConfigMap {
	record := &Record{
		Version: RecordVersion, InstallationID: "abc123",
		Provider: ProviderExternal, Cluster: cluster, Ownership: ownership,
		Existing: &ExistingClusterRecord{IngressClassName: "nginx"},
	}
	canonical, _ := record.CanonicalYAML()
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: bundle.RecordName, Namespace: bundle.Namespace},
		Data:       map[string]string{bundle.RecordKey: canonical},
	}
}

func skaliNamespace() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bundle.Namespace}}
}

func TestDetectClusterStates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// No skali-system namespace: fresh.
	fresh := &kube.Client{Clientset: k8sfake.NewSimpleClientset()}
	detection, err := DetectCluster(ctx, fresh)
	require.NoError(t, err)
	require.Equal(t, ClusterFresh, detection.State)

	// Namespace but no record: still fresh (a half-torn-down install).
	nsOnly := &kube.Client{Clientset: k8sfake.NewSimpleClientset(skaliNamespace())}
	detection, err = DetectCluster(ctx, nsOnly)
	require.NoError(t, err)
	require.Equal(t, ClusterFresh, detection.State)

	// An existing-cluster record: installed.
	installed := &kube.Client{Clientset: k8sfake.NewSimpleClientset(
		skaliNamespace(), recordConfigMap(OwnershipExistingCluster, "prod"))}
	detection, err = DetectCluster(ctx, installed)
	require.NoError(t, err)
	require.Equal(t, ClusterInstalled, detection.State)
	require.Equal(t, "prod", detection.Record.Cluster)

	// A managed record: refuse existing-cluster operations.
	managed := &kube.Client{Clientset: k8sfake.NewSimpleClientset(
		skaliNamespace(), recordConfigMap(OwnershipManaged, "prod"))}
	detection, err = DetectCluster(ctx, managed)
	require.NoError(t, err)
	require.Equal(t, ClusterManaged, detection.State)
}

func TestRecordFromExistingConfigRoundTrip(t *testing.T) {
	t.Parallel()
	config, err := ParseExistingClusterConfig([]byte(validExistingConfig +
		"storage:\n  className: fast\ndatabase:\n  tier: asynchronous\n"))
	require.NoError(t, err)
	record := RecordFromExistingConfig(config, nil)
	require.Equal(t, ProviderExternal, record.Provider)
	require.Equal(t, OwnershipExistingCluster, record.Ownership)
	require.NotNil(t, record.Existing)
	require.Equal(t, "nginx", record.Existing.IngressClassName)
	require.Equal(t, "fast", record.Existing.StorageClassName)
	require.Equal(t, "asynchronous", record.Existing.DatabaseTier)

	// CanonicalYAML is deterministic (the bundle-hash invariant): the same
	// record renders identical bytes.
	first, err := record.CanonicalYAML()
	require.NoError(t, err)
	second, err := record.CanonicalYAML()
	require.NoError(t, err)
	require.Equal(t, first, second)

	// A prior record's installation id is reused, so repeat installs stay
	// convergent.
	prior := &Record{InstallationID: "keep-me"}
	require.Equal(t, "keep-me", RecordFromExistingConfig(config, prior).InstallationID)
}

func TestPreflightExistingClusterRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	config, err := ParseExistingClusterConfig([]byte(validExistingConfig))
	require.NoError(t, err)

	// A missing IngressClass is refused (the fake reports no version, so
	// the version gate is skipped and preflight reaches the class check).
	client := &kube.Client{Clientset: k8sfake.NewSimpleClientset()}
	err = PreflightExistingCluster(ctx, client, config, nil)
	require.ErrorContains(t, err, `no IngressClass named "nginx"`)
}

func TestOwnedOperatorNamespaces(t *testing.T) {
	t.Parallel()
	// Reused operators keep their namespaces; installed ones are removed.
	both := &Record{Existing: &ExistingClusterRecord{
		Operators: OperatorsRecord{CNPG: "install", CertManager: "install"}}}
	require.Equal(t, bundle.OperatorNamespaces, ownedOperatorNamespaces(both))

	reuse := &Record{Existing: &ExistingClusterRecord{
		Operators: OperatorsRecord{CNPG: "use-existing", CertManager: "install"}}}
	require.Equal(t, []string{"cert-manager"}, ownedOperatorNamespaces(reuse))

	reuseBoth := &Record{Existing: &ExistingClusterRecord{
		Operators: OperatorsRecord{CNPG: "use-existing", CertManager: "use-existing"}}}
	require.Empty(t, ownedOperatorNamespaces(reuseBoth))
}

func TestUninstallExistingClusterStopsReconcilerFirst(t *testing.T) {
	ctx := context.Background()
	client := &kube.Client{Clientset: k8sfake.NewSimpleClientset()}
	record := &Record{Existing: &ExistingClusterRecord{
		Operators: OperatorsRecord{CNPG: "use-existing", CertManager: "use-existing"},
	}}
	progress := &recordingProgress{}

	require.NoError(t, UninstallExistingClusterBundle(ctx, client, record, progress))
	require.NotEmpty(t, progress.starts)
	require.Equal(t, reconcilerStopTitle, progress.starts[0])
}
