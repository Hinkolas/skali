package bundle

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// TestLonghornManifest pins the vendored asset against the two byte-patches
// and the naive document splitter: an asset bump that breaks either shows
// up here, not as a silently misconfigured cluster.
func TestLonghornManifest(t *testing.T) {
	t.Parallel()
	objects, err := ParseManifest(LonghornManifest())
	require.NoError(t, err)
	require.Len(t, objects, 51, "the v1.12.1 manifest parses to 51 objects; a bump must revisit the patches and this pin")

	configMaps := map[string]map[string]any{}
	kinds := map[string]int{}
	for _, object := range objects {
		kinds[object.GetKind()]++
		if object.GetKind() == "ConfigMap" {
			configMaps[object.GetName()] = object.Object
		}
	}
	require.Positive(t, kinds["CustomResourceDefinition"])
	require.Positive(t, kinds["DaemonSet"], "longhorn-manager must survive the splitter")

	// The shipped storage class must never be the cluster default:
	// local-path keeps that role for CNPG, seaweed, and skali-db. Parse the
	// inner document rather than substring-matching so indentation drift in
	// a version bump cannot fake a pass.
	storageClass := innerYAML(t, configMaps, "longhorn-storageclass", "storageclass.yaml")
	metadata, ok := storageClass["metadata"].(map[string]any)
	require.True(t, ok)
	annotations, ok := metadata["annotations"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "false", annotations["storageclass.kubernetes.io/is-default-class"])

	// Disk creation must be confined to labeled nodes so only
	// application-capable nodes hold replica data.
	settings := innerYAML(t, configMaps, "longhorn-default-setting", "default-setting.yaml")
	require.Equal(t, "true", settings["create-default-disk-on-labeled-nodes"])
	require.Equal(t, "longhorn-critical", settings["priority-class"],
		"the patch must append, never replace, the shipped settings")

	require.Contains(t, OperatorNamespaces, "longhorn-system")
}

// innerYAML unmarshals one embedded YAML document out of a ConfigMap's data.
func innerYAML(t *testing.T, configMaps map[string]map[string]any, name, key string) map[string]any {
	t.Helper()
	object, ok := configMaps[name]
	require.True(t, ok, "ConfigMap %s must exist", name)
	data, ok := object["data"].(map[string]any)
	require.True(t, ok, name)
	text, ok := data[key].(string)
	require.True(t, ok, "%s must carry %s", name, key)
	var inner map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(text), &inner))
	return inner
}

// TestStorageStage covers the skali-app StorageClass render: replica count
// from the profile, never the default class, and zero bytes locally.
func TestStorageStage(t *testing.T) {
	t.Parallel()
	require.Empty(t, storageYAML(localProfile()), "dev keeps every claim on local-path")

	for _, replicas := range []int{1, 2, 3} {
		profile := productionProfile()
		profile.Production.StorageReplicas = replicas
		objects, err := Render(profile)
		require.NoError(t, err)
		require.Len(t, objects.Storage, 1)
		class := objects.Storage[0]
		require.Equal(t, "StorageClass", class.GetKind())
		require.Equal(t, StorageClassName, class.GetName())
		require.Equal(t, "false", class.GetAnnotations()["storageclass.kubernetes.io/is-default-class"])
		parameters, _, err := unstructured.NestedStringMap(class.Object, "parameters")
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"numberOfReplicas":    strconv.Itoa(replicas),
			"staleReplicaTimeout": "30",
			"fsType":              "ext4",
			"dataLocality":        "best-effort",
			"dataEngine":          "v1",
		}, parameters)
		expansion, _, _ := unstructured.NestedBool(class.Object, "allowVolumeExpansion")
		require.True(t, expansion)
	}
}

// TestRegistryStorageShapes pins both registry renders: the legacy
// local-path shape byte-compatible with pre-Longhorn installations (node
// pin, no class) and the Longhorn shape (class, no pin).
func TestRegistryStorageShapes(t *testing.T) {
	t.Parallel()
	legacy := productionProfile()
	legacy.Production.RegistryNode = "node-a"
	source := registryYAML(legacy)
	require.Contains(t, source, "kubernetes.io/hostname: node-a")
	require.NotContains(t, source, "storageClassName")

	migrated := productionProfile()
	migrated.Production.RegistryNode = "node-a"
	migrated.Production.RegistryStorageClass = StorageClassName
	source = registryYAML(migrated)
	require.Contains(t, source, "storageClassName: "+StorageClassName)
	require.NotContains(t, source, "kubernetes.io/hostname",
		"the Longhorn volume attaches wherever the pod schedules; the pin must go")
	require.Contains(t, source, "skali.dev/capability-registry",
		"capability placement stays")

	// Both shapes must move the hash: converge follows the profile.
	require.NotEqual(t, Hash(legacy), Hash(migrated))
}

// TestLonghornHashProperties: the vendored manifest is a production hash
// input and must never leak into the local fingerprint.
func TestLonghornHashProperties(t *testing.T) {
	t.Parallel()
	base := productionProfile()
	replicas := productionProfile()
	replicas.Production.StorageReplicas = 1
	require.NotEqual(t, Hash(base), Hash(replicas), "storage replicas must move the production hash")

	manifest := string(LonghornManifest())
	require.True(t, strings.Contains(manifest, "longhornio/longhorn-manager:v"+LonghornVersion),
		"the exported version pin must match the vendored manifest")
}
