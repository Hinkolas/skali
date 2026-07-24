package installer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
)

func cnpgCluster(instances, ready int64, phase string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": "skali-db", "namespace": bundle.Namespace},
		"spec":       map[string]any{"instances": instances},
		"status":     map[string]any{"phase": phase, "readyInstances": ready},
	}}
}

func healthyDeployment(name string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: bundle.Namespace},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1},
	}
}

func diagnoseClient(clientObjects []runtime.Object, dynamicObjects ...runtime.Object) *kube.Client {
	scheme := runtime.NewScheme()
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}:  "ClusterList",
			{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}: "CertificateList",
		}, dynamicObjects...)
	return &kube.Client{
		Clientset: k8sfake.NewSimpleClientset(clientObjects...),
		Dynamic:   dynamic,
	}
}

// diagnoseHost is a managed server whose unit answers active, whose k3s
// matches the pin, and whose registries.yaml is complete.
func diagnoseHost(t *testing.T) *host.Fake {
	t.Helper()
	fake := withRecord(t, withK3s(linuxHost(), "k3s.service", true), layout.RoleServer)
	fake.FS[K3sRegistriesPath] = []byte(k3sRegistriesYAML("pull-secret-value"))
	record, err := LoadRecord(context.Background(), fake)
	require.NoError(t, err)
	record.Versions.Bundle = "test"
	require.NoError(t, SaveRecord(context.Background(), fake, record))
	fake.Writes = nil
	return fake
}

func findCheck(t *testing.T, diagnosis *Diagnosis, name string) Check {
	t.Helper()
	for _, check := range diagnosis.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("no check named %q in %+v", name, diagnosis.Checks)
	return Check{}
}

func TestDiagnoseHealthy(t *testing.T) {
	t.Parallel()
	client := diagnoseClient(
		[]runtime.Object{liveProfileNode(), healthyDeployment("skali-registry"), healthyDeployment("skalid")},
		cnpgCluster(1, 1, "Cluster in healthy state"),
	)
	diagnosis, err := Diagnose(context.Background(), diagnoseHost(t), DiagnoseOptions{Client: client})
	require.NoError(t, err)
	require.Zero(t, diagnosis.Fails())
	require.Empty(t, diagnosis.Suggestions)
	require.Equal(t, "active", findCheck(t, diagnosis, "k3s service").Detail)
	require.Equal(t, "reachable", findCheck(t, diagnosis, "kubernetes api").Detail)
	require.Equal(t, "1/1 ready", findCheck(t, diagnosis, "nodes").Detail)
	require.Equal(t, "healthy (single)", findCheck(t, diagnosis, "bootstrap database").Detail)
	require.Equal(t, "healthy", findCheck(t, diagnosis, "skalid").Detail)
}

func TestDiagnoseReadOnly(t *testing.T) {
	t.Parallel()
	clientset := k8sfake.NewSimpleClientset(
		liveProfileNode(), healthyDeployment("skali-registry"), healthyDeployment("skalid"))
	scheme := runtime.NewScheme()
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}:  "ClusterList",
			{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}: "CertificateList",
		}, cnpgCluster(1, 1, "Cluster in healthy state"))
	client := &kube.Client{Clientset: clientset, Dynamic: dynamic}

	_, err := Diagnose(context.Background(), diagnoseHost(t), DiagnoseOptions{Client: client})
	require.NoError(t, err)
	verbs := func(actions []k8stesting.Action) {
		for _, action := range actions {
			verb := action.GetVerb()
			require.Contains(t, []string{"get", "list"}, verb,
				"diagnose must stay read-only, saw verb %q on %s", verb, action.GetResource().Resource)
		}
	}
	verbs(clientset.Actions())
	verbs(dynamic.Actions())
}

func TestDiagnoseHostOnlyWhenUnitDown(t *testing.T) {
	t.Parallel()
	fake := withRecord(t, withK3s(linuxHost(), "k3s.service", false), layout.RoleServer)
	fake.FS[K3sRegistriesPath] = []byte(k3sRegistriesYAML(""))

	diagnosis, err := Diagnose(context.Background(), fake, DiagnoseOptions{})
	require.NoError(t, err)
	require.Equal(t, 2, diagnosis.Fails(), "unit down and credential missing")
	require.Equal(t, SeverityFail, findCheck(t, diagnosis, "k3s service").Severity)
	require.Contains(t, findCheck(t, diagnosis, "registry mirror").Detail, "node pull credential")
	require.Contains(t, diagnosis.Suggestions, "skali cluster repair")
	for _, check := range diagnosis.Checks {
		require.NotEqual(t, "kubernetes api", check.Name,
			"the kubernetes ladder must not run while the unit is down")
	}
}

func TestDiagnoseVersionDriftWarns(t *testing.T) {
	t.Parallel()
	fake := withRecord(t, withK3s(linuxHost(), "k3s.service", true), layout.RoleServer)
	fake.FS[K3sRegistriesPath] = []byte(k3sRegistriesYAML("pull-secret-value"))
	record, err := LoadRecord(context.Background(), fake)
	require.NoError(t, err)
	record.Versions.Bundle = "test"
	require.NoError(t, SaveRecord(context.Background(), fake, record))
	fake.Handlers["k3s"] = func(cmd host.Command) (host.Result, error) {
		return host.Result{Stdout: "k3s version v1.33.2+k3s1 (0000)\n"}, nil
	}
	client := diagnoseClient(
		[]runtime.Object{liveProfileNode(), healthyDeployment("skali-registry"), healthyDeployment("skalid")},
		cnpgCluster(1, 1, "Cluster in healthy state"),
	)
	diagnosis, err := Diagnose(context.Background(), fake, DiagnoseOptions{Client: client})
	require.NoError(t, err)
	require.Zero(t, diagnosis.Fails(), "a version drift warns, never fails")
	require.Equal(t, SeverityWarn, findCheck(t, diagnosis, "k3s version").Severity)
	require.Contains(t, diagnosis.Suggestions, "skali cluster upgrade")
}

func TestDiagnoseBeforeInitializationSkipsMissingBundle(t *testing.T) {
	t.Parallel()
	fake := withRecord(t, withK3s(linuxHost(), "k3s.service", true), layout.RoleServer)
	fake.FS[K3sRegistriesPath] = []byte(k3sRegistriesYAML("pull-secret-value"))
	client := diagnoseClient([]runtime.Object{liveProfileNode()})

	diagnosis, err := Diagnose(context.Background(), fake, DiagnoseOptions{Client: client})
	require.NoError(t, err)
	require.Zero(t, diagnosis.Fails())
	require.Equal(t, "not initialized yet (expected)",
		findCheck(t, diagnosis, "platform bundle").Detail)
	for _, name := range []string{"bootstrap database", "managed registry", "skalid"} {
		for _, check := range diagnosis.Checks {
			require.NotEqual(t, name, check.Name)
		}
	}
	require.Empty(t, diagnosis.Suggestions)
}

func TestDiagnoseSectionSevenScene(t *testing.T) {
	t.Parallel()
	// The transcript scene: database degraded with a pending pod naming a
	// storage problem, skalid crashlooping because of it.
	pendingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "skali-db-2", Namespace: bundle.Namespace,
			Labels: map[string]string{"cnpg.io/cluster": "skali-db"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
				Message: "0/3 nodes available: insufficient storage on db-2",
			}},
		},
	}
	crashPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "skalid-abc", Namespace: bundle.Namespace,
			Labels: map[string]string{"app.kubernetes.io/name": "skalid"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "CrashLoopBackOff",
				}},
			}},
		},
	}
	brokenSkalid := healthyDeployment("skalid")
	brokenSkalid.Status.AvailableReplicas = 0
	client := diagnoseClient(
		[]runtime.Object{liveProfileNode(), healthyDeployment("skali-registry"), brokenSkalid,
			pendingPod, crashPod},
		cnpgCluster(3, 1, "Waiting for the instances to become active"),
	)

	diagnosis, err := Diagnose(context.Background(), diagnoseHost(t), DiagnoseOptions{Client: client})
	require.NoError(t, err)
	require.Equal(t, 2, diagnosis.Fails())

	database := findCheck(t, diagnosis, "bootstrap database")
	require.Equal(t, SeverityFail, database.Severity)
	require.Equal(t, "1/3 instances ready", database.Detail)
	require.Len(t, database.Sub, 1)
	require.Contains(t, database.Sub[0], "pod skali-system/skali-db-2: Pending")
	require.Contains(t, database.Sub[0], "insufficient storage on db-2")

	skalid := findCheck(t, diagnosis, "skalid")
	require.Equal(t, "CrashLoopBackOff (cannot reach its database)", skalid.Detail)

	require.Contains(t, diagnosis.Suggestions,
		"free or expand storage on the named node, then: skali cluster repair")
}

func TestDiagnoseAgentSkipsKubernetes(t *testing.T) {
	t.Parallel()
	fake := withRecord(t, withK3s(linuxHost(), "k3s-agent.service", true), layout.RoleAgent)
	fake.FS[K3sRegistriesPath] = []byte(k3sRegistriesYAML("pull-secret-value"))
	diagnosis, err := Diagnose(context.Background(), fake, DiagnoseOptions{})
	require.NoError(t, err)
	require.Zero(t, diagnosis.Fails())
	for _, check := range diagnosis.Checks {
		require.NotEqual(t, "kubernetes api", check.Name, "agents have no kube api access by design")
	}
}
