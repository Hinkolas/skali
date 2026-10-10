package cnpg

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/Hinkolas/skali/internal/dbcatalog"
	"github.com/Hinkolas/skali/internal/layout"
)

func clusterSpec() ClusterSpec {
	return ClusterSpec{
		Namespace:    "skali-platform",
		Name:         "pg17-shared",
		Image:        "ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie",
		Instances:    1,
		StorageBytes: 10 << 30,
		Roles: []Role{
			{Name: "u_data_abcd1234", SecretName: "dbcred-abcd1234", Login: true},
		},
	}
}

// TestRenderClusterRotationRoles pins the role shapes a credential rotation
// renders: an owner that no longer logs in (password wiped, no Secret), a
// login role under it with its own Secret, and nothing else CNPG could act
// on behind the worker's back.
func TestRenderClusterRotationRoles(t *testing.T) {
	t.Parallel()
	spec := clusterSpec()
	spec.Roles = []Role{
		{Name: "u_data_abcd1234", DisablePassword: true},
		{Name: "u_data_abcd1234_v2", SecretName: "dbcred-abcd1234-v2", Login: true, InRoles: []string{"u_data_abcd1234"}},
	}
	object := RenderCluster(spec).Object
	roles, _, _ := unstructured.NestedSlice(object, "spec", "managed", "roles")
	require.Len(t, roles, 2)

	owner := roles[0].(map[string]any)
	require.Equal(t, "u_data_abcd1234", owner["name"])
	require.Equal(t, "present", owner["ensure"])
	require.Equal(t, false, owner["login"])
	require.Equal(t, true, owner["inherit"])
	require.Equal(t, int64(-1), owner["connectionLimit"], "the CRD's default, so an unchanged pool applies nothing")
	require.Equal(t, true, owner["disablePassword"])
	_, hasSecret := owner["passwordSecret"]
	require.False(t, hasSecret, "an owner without a login has no Secret to point at")
	_, hasMembers := owner["inRoles"]
	require.False(t, hasMembers)

	login := roles[1].(map[string]any)
	require.Equal(t, "u_data_abcd1234_v2", login["name"])
	require.Equal(t, true, login["login"])
	require.Equal(t, map[string]any{"name": "dbcred-abcd1234-v2"}, login["passwordSecret"])
	require.Equal(t, []any{"u_data_abcd1234"}, login["inRoles"])
	require.Equal(t, int64(-1), login["connectionLimit"])
	_, disabled := login["disablePassword"]
	require.False(t, disabled)
}

// A pool's Services target each port's own number, as the server defaults
// an unset target, so an unchanged Service applies nothing.
func TestRenderPoolServicesTargetTheirPorts(t *testing.T) {
	t.Parallel()
	for _, service := range []*corev1.Service{
		RenderMetricsService("skali-platform", "pg17-shared"),
		RenderPrimaryNodePortService("skali-platform", "pg17-shared", 30432),
	} {
		for _, port := range service.Spec.Ports {
			require.Equal(t, intstr.FromInt32(port.Port), port.TargetPort, "%s/%s", service.Name, port.Name)
		}
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
	class, _, _ := unstructured.NestedString(object, "spec", "priorityClassName")
	require.Equal(t, layout.PriorityClassCritical, class)
	hibernation, _, _ := unstructured.NestedString(object, "metadata", "annotations", HibernationAnnotation)
	require.Equal(t, "off", hibernation,
		"the annotation stays an explicit off for one release so SSA wakes previously hibernated pools")

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

func TestRenderClusterParametersAndResources(t *testing.T) {
	t.Parallel()
	spec := clusterSpec()
	spec.Parameters = map[string]string{"shared_buffers": "1024MB", "work_mem": "10MB"}
	spec.MemoryRequestBytes = 4 << 30
	object := RenderCluster(spec).Object

	parameters, _, _ := unstructured.NestedStringMap(object, "spec", "postgresql", "parameters")
	require.Equal(t, map[string]string{"shared_buffers": "1024MB", "work_mem": "10MB"}, parameters)
	_, found, _ := unstructured.NestedMap(object, "spec", "postgresql", "synchronous")
	require.False(t, found, "parameters alone render no synchronous block")
	memory, _, _ := unstructured.NestedString(object, "spec", "resources", "requests", "memory")
	require.Equal(t, "4Gi", memory, "kubernetes units on the request, PostgreSQL units in the parameters")
	_, found, _ = unstructured.NestedMap(object, "spec", "resources", "limits")
	require.False(t, found, "no limit: a tight cap would OOM-kill the primary")

	// Both parameters and quorum replication share the one postgresql block.
	spec.Instances = 3
	spec.Synchronous = true
	object = RenderCluster(spec).Object
	parameters, _, _ = unstructured.NestedStringMap(object, "spec", "postgresql", "parameters")
	require.Equal(t, "1024MB", parameters["shared_buffers"])
	method, _, _ := unstructured.NestedString(object, "spec", "postgresql", "synchronous", "method")
	require.Equal(t, "any", method)
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

// TestCatalog pins the driver to dbcatalog: every major dbcatalog offers has
// a blessed image here, and the image's extension list is dbcatalog's.
func TestCatalog(t *testing.T) {
	t.Parallel()
	require.Equal(t, dbcatalog.Majors("postgres"), SupportedMajors("postgres"))
	for _, major := range dbcatalog.Majors("postgres") {
		image, ok := Lookup("postgres", major)
		require.True(t, ok, "major %d has no blessed image", major)
		require.NotEmpty(t, image.Ref)
		require.Equal(t, dbcatalog.Extensions("postgres", major), image.Extensions)
		require.Contains(t, image.Extensions, "vector")
	}
	_, ok := Lookup("postgres", 12)
	require.False(t, ok)
}
