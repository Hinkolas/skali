package cnpg

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func clusterSpec() ClusterSpec {
	return ClusterSpec{
		Namespace:    "skali-platform",
		Name:         "pg17-shared",
		Image:        "ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie",
		Instances:    1,
		StorageBytes: 10 << 30,
		Roles: []Role{
			{Name: "u_data_abcd1234", SecretName: "dbcred-abcd1234"},
		},
	}
}

func TestRenderClusterSingle(t *testing.T) {
	t.Parallel()
	object := RenderCluster(clusterSpec()).Object

	instances, _, _ := unstructured.NestedInt64(object, "spec", "instances")
	require.EqualValues(t, 1, instances)
	image, _, _ := unstructured.NestedString(object, "spec", "imageName")
	require.Equal(t, "ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie", image)
	size, _, _ := unstructured.NestedString(object, "spec", "storage", "size")
	require.Equal(t, "10Gi", size)
	hibernation, _, _ := unstructured.NestedString(object, "metadata", "annotations", HibernationAnnotation)
	require.Equal(t, "off", hibernation, "hibernation is explicit in both states so SSA owns the field")

	// Single instance renders no synchronous block; local shape renders no
	// node selector.
	_, found, _ := unstructured.NestedMap(object, "spec", "postgresql")
	require.False(t, found)
	_, found, _ = unstructured.NestedMap(object, "spec", "affinity")
	require.False(t, found)

	roles, _, _ := unstructured.NestedSlice(object, "spec", "managed", "roles")
	require.Len(t, roles, 1)
	role := roles[0].(map[string]any)
	require.Equal(t, "u_data_abcd1234", role["name"])
	require.Equal(t, true, role["login"])
	require.Equal(t, map[string]any{"name": "dbcred-abcd1234"}, role["passwordSecret"])

	labels, _, _ := unstructured.NestedStringMap(object, "metadata", "labels")
	require.Equal(t, "true", labels["skali.dev/managed"])
	require.Equal(t, "pg17-shared", labels["skali.dev/pool"])
}

func TestRenderClusterSynchronousManaged(t *testing.T) {
	t.Parallel()
	spec := clusterSpec()
	spec.Instances = 3
	spec.Synchronous = true
	spec.Managed = true
	object := RenderCluster(spec).Object

	method, _, _ := unstructured.NestedString(object, "spec", "postgresql", "synchronous", "method")
	require.Equal(t, "any", method)
	number, _, _ := unstructured.NestedInt64(object, "spec", "postgresql", "synchronous", "number")
	require.EqualValues(t, 1, number)
	selector, _, _ := unstructured.NestedStringMap(object, "spec", "affinity", "nodeSelector")
	require.Equal(t, map[string]string{"skali.dev/capability-database": "true"}, selector)
}

func TestRenderClusterHibernated(t *testing.T) {
	t.Parallel()
	spec := clusterSpec()
	spec.Hibernated = true
	object := RenderCluster(spec).Object
	hibernation, _, _ := unstructured.NestedString(object, "metadata", "annotations", HibernationAnnotation)
	require.Equal(t, "on", hibernation)
}

func TestRenderDatabase(t *testing.T) {
	t.Parallel()
	object := RenderDatabase(DatabaseSpec{
		Namespace:    "skali-platform",
		ObjectName:   "db-abcd1234",
		ClusterName:  "pg17-shared",
		DatabaseName: "db_data_abcd1234",
		Owner:        "u_data_abcd1234",
		Extensions:   []string{"pg_trgm", "citext"},
		Labels: map[string]string{
			"skali.dev/claim":       "0f0f",
			"skali.dev/environment": "1e1e",
			"skali.dev/service":     "databases.data",
		},
	}).Object

	cluster, _, _ := unstructured.NestedString(object, "spec", "cluster", "name")
	require.Equal(t, "pg17-shared", cluster)
	name, _, _ := unstructured.NestedString(object, "spec", "name")
	require.Equal(t, "db_data_abcd1234", name)
	owner, _, _ := unstructured.NestedString(object, "spec", "owner")
	require.Equal(t, "u_data_abcd1234", owner)
	ensure, _, _ := unstructured.NestedString(object, "spec", "ensure")
	require.Equal(t, "present", ensure)
	reclaim, _, _ := unstructured.NestedString(object, "spec", "databaseReclaimPolicy")
	require.Equal(t, "delete", reclaim, "teardown must drop the logical database")

	extensions, _, _ := unstructured.NestedSlice(object, "spec", "extensions")
	require.Len(t, extensions, 2)
	require.Equal(t, map[string]any{"name": "pg_trgm", "ensure": "present"}, extensions[0])

	labels, _, _ := unstructured.NestedStringMap(object, "metadata", "labels")
	require.Equal(t, "true", labels["skali.dev/managed"])
	require.Equal(t, "databases.data", labels["skali.dev/service"])
	require.Equal(t, "pg17-shared", labels["skali.dev/pool"])
}

func TestRenderDatabaseAbsent(t *testing.T) {
	t.Parallel()
	object := RenderDatabase(DatabaseSpec{
		Namespace: "skali-platform", ObjectName: "db-x", ClusterName: "p",
		DatabaseName: "d", Owner: "o", Absent: true,
	}).Object
	ensure, _, _ := unstructured.NestedString(object, "spec", "ensure")
	require.Equal(t, "absent", ensure)
}

func TestRenderCredentialSecret(t *testing.T) {
	t.Parallel()
	secret := RenderCredentialSecret("skali-platform", "dbcred-abcd1234", "pg17-shared",
		"u_data_abcd1234", "s3cr3t", map[string]string{"skali.dev/claim": "0f0f"})
	require.Equal(t, "kubernetes.io/basic-auth", string(secret.Type))
	require.Equal(t, "u_data_abcd1234", string(secret.Data["username"]))
	require.Equal(t, "s3cr3t", string(secret.Data["password"]))
	require.Equal(t, "pg17-shared", secret.Labels["skali.dev/pool"])
	require.Equal(t, "0f0f", secret.Labels["skali.dev/claim"])
}

func TestCatalog(t *testing.T) {
	t.Parallel()
	image, ok := Lookup("postgres", 17)
	require.True(t, ok)
	require.NotEmpty(t, image.Ref)
	require.True(t, image.SupportsExtension("pg_trgm"))
	require.False(t, image.SupportsExtension("postgis"), "postgis waits for a blessed image")
	require.False(t, image.SupportsExtension("vector"))
	_, ok = Lookup("postgres", 12)
	require.False(t, ok)
	require.Equal(t, []int{17, 18}, SupportedMajors("postgres"))
}
