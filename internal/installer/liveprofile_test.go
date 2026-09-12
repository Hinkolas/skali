package installer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
)

func liveProfileRecord() *Record {
	return &Record{
		Version:        RecordVersion,
		InstallationID: "0f0f0f0f",
		Provider:       ProviderK3s,
		Cluster:        "e2e",
		Ownership:      OwnershipManaged,
		Node: NodeRecord{
			Name: "cp-1", Role: layout.RoleServer,
			Capabilities: layout.Capabilities,
		},
		Endpoints:     &Endpoints{API: "skali.e2e.test", Registry: "registry.skali.e2e.test"},
		TLS:           &TLSConfig{IssuerEmail: "e2e@skali.e2e.test", ACMEServer: "https://acme.test/directory"},
		StorageDriver: bundle.StorageDriverLonghorn,
		Versions:      Versions{Installer: "v0.0.0-dev", K3s: K3sVersion, Bundle: "0.0.0-dev"},
	}
}

func liveProfileNode() *corev1.Node {
	labels := layout.CapabilityLabels(layout.Capabilities)
	labels[layout.ClusterLabel] = "e2e"
	labels[layout.ControlPlaneLabel] = "true"
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-1", Labels: labels},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: "Ready", Status: corev1.ConditionTrue}},
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: K3sVersion},
		},
	}
}

func liveProfileObjects() []runtime.Object {
	return []runtime.Object{
		liveProfileNode(),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "skali-auth", Namespace: bundle.Namespace},
			Data:       map[string][]byte{"AUTH_SECRET": []byte("auth-secret-value")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "skali-registry-token", Namespace: bundle.Namespace},
			Data: map[string][]byte{
				"key.pem": []byte("key-pem"), "cert.pem": []byte("cert-pem"),
				"node-secret": []byte("pull-secret-value"),
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "skalid", Namespace: bundle.Namespace},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{"skali.dev/image-id": "sha256:abc"},
					},
					Spec: corev1.PodSpec{Containers: []corev1.Container{
						{Name: "skalid", Image: "skalid:dev"},
					}},
				},
			},
		},
	}
}

func fakeClientWith(objects ...runtime.Object) *kube.Client {
	return &kube.Client{Clientset: k8sfake.NewSimpleClientset(objects...)}
}

// TestLiveProfileHashStability is the load-bearing invariant: the profile
// LiveProfile reconstructs must hash identically to the profile Init
// builds from the same inputs, or tier apply and repair would restamp a
// hash that misrepresents the live cluster.
func TestLiveProfileHashStability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record := liveProfileRecord()
	client := fakeClientWith(liveProfileObjects()...)
	fake := &host.Fake{FS: map[string][]byte{
		K3sRegistriesPath: []byte(k3sRegistriesYAML("pull-secret-value")),
	}}

	profile, live, err := LiveProfile(ctx, client, fake, record)
	require.NoError(t, err)

	// The Init-built equivalent, mirroring initrun.go's construction from
	// the identical inputs.
	node := liveProfileNode()
	expectedLayout := LayoutFromNodes([]corev1.Node{*node}, record.Cluster)
	topology := expectedLayout.Topology()
	canonical, err := record.CanonicalYAML()
	require.NoError(t, err)
	expected := bundle.Profile{
		SkalidImage:   "skalid:dev",
		SkalidImageID: "sha256:abc",
		AuthSecret:    "auth-secret-value",
		RegistryHost:  bundle.RegistryInternalHost,
		Production: &bundle.Production{
			ClusterName:     record.Cluster,
			IngressHost:     record.Endpoints.API,
			RegistryDomain:  record.Endpoints.Registry,
			TokenKeyPEM:     "key-pem",
			TokenCertPEM:    "cert-pem",
			NodePullSecret:  "pull-secret-value",
			ACMEEmail:       record.TLS.IssuerEmail,
			ACMEServer:      record.TLS.ACMEServer,
			Capabilities:    layout.UnionCapabilities(expectedLayout.Nodes),
			DatabaseTier:    topology.DatabaseTier,
			DatabaseStorage: DefaultDatabaseStorage,
			RegistryStorage: DefaultRegistryStorage,
			StorageDriver:   bundle.StorageDriverLonghorn,
			StorageReplicas: layout.StorageReplicas(topology.Capable[layout.CapabilityApplication]),
			// No registry claim exists in the fake, so the profile selects
			// the Longhorn shape, exactly like a fresh Init.
			RegistryStorageClass: bundle.StorageClassName,

			InstallationRecord: canonical,
		},
	}
	require.Equal(t, expected, profile)
	require.Equal(t, bundle.Hash(expected), bundle.Hash(profile))
	require.Equal(t, topology.DatabaseTier, live.Topology().DatabaseTier)
}

// TestLiveProfileRegistryStorageClass pins the migration-safety contract:
// the profile renders the registry shape the cluster already has, because
// the class is immutable on an existing claim and a converge that renders
// anything else would wedge forever.
func TestLiveProfileRegistryStorageClass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &host.Fake{FS: map[string][]byte{
		K3sRegistriesPath: []byte(k3sRegistriesYAML("pull-secret-value")),
	}}

	legacyClass := "local-path"
	legacyClaim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "skali-registry-data", Namespace: bundle.Namespace},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &legacyClass},
	}
	objects := append(liveProfileObjects(), legacyClaim)
	profile, _, err := LiveProfile(ctx, fakeClientWith(objects...), fake, liveProfileRecord())
	require.NoError(t, err)
	require.Empty(t, profile.Production.RegistryStorageClass,
		"a legacy local-path claim must keep the legacy registry shape")

	migratedClass := bundle.StorageClassName
	migratedClaim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "skali-registry-data", Namespace: bundle.Namespace},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &migratedClass},
	}
	objects = append(liveProfileObjects(), migratedClaim)
	profile, _, err = LiveProfile(ctx, fakeClientWith(objects...), fake, liveProfileRecord())
	require.NoError(t, err)
	require.Equal(t, bundle.StorageClassName, profile.Production.RegistryStorageClass)

	// A record without a storage driver reads as local: no skali-app
	// registry shape even with the claim absent, and the profile carries
	// the local driver.
	legacyRecord := liveProfileRecord()
	legacyRecord.StorageDriver = ""
	profile, _, err = LiveProfile(ctx, fakeClientWith(liveProfileObjects()...), fake, legacyRecord)
	require.NoError(t, err)
	require.Equal(t, bundle.StorageDriverLocal, profile.Production.StorageDriver)
	require.Empty(t, profile.Production.RegistryStorageClass,
		"the local driver always keeps the legacy registry shape")
}

func TestLiveProfileRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Missing record answers.
	bare := liveProfileRecord()
	bare.Endpoints = nil
	_, _, err := LiveProfile(ctx, fakeClientWith(liveProfileObjects()...), &host.Fake{}, bare)
	require.ErrorContains(t, err, "missing endpoints or tls answers")

	// Missing auth secret.
	objects := liveProfileObjects()
	_, _, err = LiveProfile(ctx, fakeClientWith(objects[0], objects[2], objects[3]),
		&host.Fake{}, liveProfileRecord())
	require.ErrorContains(t, err, "auth secret is missing")

	// Missing pull credential in registries.yaml.
	fake := &host.Fake{FS: map[string][]byte{
		K3sRegistriesPath: []byte(k3sRegistriesYAML("")),
	}}
	_, _, err = LiveProfile(ctx, fakeClientWith(liveProfileObjects()...), fake, liveProfileRecord())
	require.ErrorContains(t, err, "missing the registry pull credential")

	// Missing skalid deployment: the image cannot be read.
	fake = &host.Fake{FS: map[string][]byte{
		K3sRegistriesPath: []byte(k3sRegistriesYAML("pull-secret-value")),
	}}
	_, _, err = LiveProfile(ctx, fakeClientWith(objects[0], objects[1], objects[2]), fake, liveProfileRecord())
	require.ErrorContains(t, err, "skalid deployment is missing")
	require.ErrorContains(t, err, "skali cluster upgrade")

	// The full profile reconstructs without a separate console deployment.
	_, _, err = LiveProfile(ctx, fakeClientWith(liveProfileObjects()...), fake, liveProfileRecord())
	require.NoError(t, err)

}
