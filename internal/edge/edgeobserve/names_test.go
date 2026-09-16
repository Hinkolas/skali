package edgeobserve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	metadatafake "k8s.io/client-go/metadata/fake"
)

func TestCovers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		names  []string
		domain string
		want   bool
	}{
		{"exact", []string{"app.example.com"}, "app.example.com", true},
		{"case and trailing dot", []string{"App.Example.com"}, "app.example.com.", true},
		{"other name", []string{"old.example.com"}, "new.example.com", false},
		{"wildcard one label", []string{"*.example.com"}, "app.example.com", true},
		{"wildcard two labels", []string{"*.example.com"}, "a.b.example.com", false},
		{"wildcard apex", []string{"*.example.com"}, "example.com", false},
		{"empty list", nil, "app.example.com", false},
		{"empty domain", []string{"app.example.com"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, Covers(tc.names, tc.domain))
		})
	}
}

func tlsSecret(annotations map[string]string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "tls-demo", Annotations: annotations},
	}
}

func TestIssuedNames(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, metav1.AddMetaToScheme(scheme))

	t.Run("annotated", func(t *testing.T) {
		client := metadatafake.NewSimpleMetadataClient(scheme, tlsSecret(map[string]string{
			annotationAltNames: "Old.example.com, alias.example.com", annotationCommonName: "old.example.com",
		}))
		names, err := IssuedNames(ctx, client, "ns", "tls-demo")
		require.NoError(t, err)
		require.Equal(t, []string{"alias.example.com", "old.example.com"}, names)
	})
	t.Run("unannotated", func(t *testing.T) {
		client := metadatafake.NewSimpleMetadataClient(scheme, tlsSecret(nil))
		names, err := IssuedNames(ctx, client, "ns", "tls-demo")
		require.NoError(t, err)
		require.Nil(t, names)
	})
	t.Run("absent", func(t *testing.T) {
		client := metadatafake.NewSimpleMetadataClient(scheme)
		names, err := IssuedNames(ctx, client, "ns", "tls-demo")
		require.NoError(t, err)
		require.Nil(t, names)
	})
	t.Run("no client", func(t *testing.T) {
		names, err := IssuedNames(ctx, nil, "ns", "tls-demo")
		require.NoError(t, err)
		require.Nil(t, names)
	})
}
