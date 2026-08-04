package installer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

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
		[]runtime.Object{liveProfileNode(), healthyDeployment("skali-registry"), healthyDeployment("skalid"), healthyDeployment("skali-web")},
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
		liveProfileNode(), healthyDeployment("skali-registry"), healthyDeployment("skalid"), healthyDeployment("skali-web"))
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
		[]runtime.Object{liveProfileNode(), healthyDeployment("skali-registry"), healthyDeployment("skalid"), healthyDeployment("skali-web")},
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
			healthyDeployment("skali-web"), pendingPod, crashPod},
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

// servingCertificate mints a k3s-shaped API certificate covering exactly
// the given addresses.
func servingCertificate(t *testing.T, addresses ...string) []byte {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "kube-apiserver"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost", "kubernetes.default"},
	}
	for _, address := range addresses {
		template.IPAddresses = append(template.IPAddresses, net.ParseIP(address))
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// multiHomedHost is a reconciled server that declared the private address,
// but whose k3s certificate and coordinator socket still only know the
// public one: the exact shape a cluster installed before the declaration
// existed has.
func multiHomedHost(t *testing.T, coordinatorBound string) *host.Fake {
	t.Helper()
	fake := diagnoseHost(t)
	record, err := LoadRecord(context.Background(), fake)
	require.NoError(t, err)
	record.Version = RecordVersionReconciled
	record.Management = ManagementReconciled
	record.Node.ID = "node-1"
	record.Node.SetNetwork(NodeNetwork{
		ClusterIP: "10.0.1.2", PublicIPs: []string{"203.0.113.7"},
	})
	record.Coordinator = &CoordinatorRecord{
		Endpoints: []string{"https://10.0.1.2:6444"}, CAPin: "sha256:test",
	}
	require.NoError(t, SaveRecord(context.Background(), fake, record))
	fake.FS[K3sServingCertPath] = servingCertificate(t, "203.0.113.7")
	fake.FS[HostdBinaryPath] = []byte("hostd")
	// The host services are healthy here; the addresses are the only
	// thing wrong, which is what isolates the two scoped repairs.
	active := map[string]bool{
		"k3s.service": true, HostdAgentUnit: true, HostdCoordinatorUnit: true,
	}
	fake.Handlers["systemctl"] = func(cmd host.Command) (host.Result, error) {
		if len(cmd.Args) == 2 && active[cmd.Args[1]] {
			return host.Result{Stdout: "active\n"}, nil
		}
		return host.Result{ExitCode: 4, Stdout: "not-found\n"}, nil
	}
	fake.Handlers["ip"] = func(cmd host.Command) (host.Result, error) {
		if len(cmd.Args) > 0 && cmd.Args[0] == "route" {
			return host.Result{Stdout: "1.1.1.1 via 203.0.113.1 dev eth0 src 203.0.113.7 uid 0\n"}, nil
		}
		return host.Result{Stdout: hetznerAddresses}, nil
	}
	fake.Handlers["ss"] = func(host.Command) (host.Result, error) {
		return host.Result{Stdout: "LISTEN 0 4096 " + coordinatorBound + ":6444 0.0.0.0:*\n"}, nil
	}
	fake.Writes = nil
	return fake
}

func TestDiagnoseMultiHomedNodeFindsUnservedEndpointAndCertificate(t *testing.T) {
	t.Parallel()
	client := diagnoseClient(
		[]runtime.Object{liveProfileNode(), healthyDeployment("skali-registry"), healthyDeployment("skalid"), healthyDeployment("skali-web")},
		cnpgCluster(1, 1, "Cluster in healthy state"),
	)

	diagnosis, err := Diagnose(context.Background(), multiHomedHost(t, "203.0.113.7"),
		DiagnoseOptions{Client: client})

	require.NoError(t, err)
	certificate := findCheck(t, diagnosis, "api certificate")
	require.Equal(t, SeverityFail, certificate.Severity)
	require.Contains(t, certificate.Detail, "does not cover 10.0.1.2")
	endpoint := findCheck(t, diagnosis, "coordinator endpoint")
	require.Equal(t, SeverityFail, endpoint.Severity)
	require.Contains(t, endpoint.Detail, "https://10.0.1.2:6444")
	require.Contains(t, diagnosis.Suggestions, "skali cluster repair")

	// Both failures map onto the two scoped repairs, and neither of them
	// touches the advertised node address.
	actions, refusals := PlanRepairs(diagnosis, RepairDeps{Runner: multiHomedHost(t, "203.0.113.7"),
		Record: diagnosis.Host.Record, HostdBinary: []byte("hostd")})
	require.Empty(t, refusals)
	var ids []string
	for _, action := range actions {
		ids = append(ids, action.ID)
	}
	require.Contains(t, ids, "widen-api-certificate")
	require.Contains(t, ids, "rebind-coordinator")
}

func TestDiagnoseMultiHomedNodeAcceptsBoundEndpoint(t *testing.T) {
	t.Parallel()
	fake := multiHomedHost(t, "10.0.1.2")
	fake.FS[K3sServingCertPath] = servingCertificate(t, "203.0.113.7", "10.0.1.2")
	client := diagnoseClient(
		[]runtime.Object{liveProfileNode(), healthyDeployment("skali-registry"), healthyDeployment("skalid"), healthyDeployment("skali-web")},
		cnpgCluster(1, 1, "Cluster in healthy state"),
	)

	diagnosis, err := Diagnose(context.Background(), fake, DiagnoseOptions{Client: client})

	require.NoError(t, err)
	require.Equal(t, SeverityOK, findCheck(t, diagnosis, "api certificate").Severity)
	require.Equal(t, SeverityOK, findCheck(t, diagnosis, "coordinator endpoint").Severity)
	require.Equal(t, "cluster address 10.0.1.2, public 203.0.113.7",
		findCheck(t, diagnosis, "node addresses").Detail)
}

// A host that never declared an address, on a machine that has more than
// one, is the shape that silently puts cluster traffic on the public
// interface. It is a warning, not a failure: the cluster works.
func TestDiagnoseUndeclaredAddressOnMultiHomedHost(t *testing.T) {
	t.Parallel()
	fake := multiHomedHost(t, "203.0.113.7")
	record, err := LoadRecord(context.Background(), fake)
	require.NoError(t, err)
	record.Node.IP = ""
	record.Node.PublicIPs = nil
	require.NoError(t, SaveRecord(context.Background(), fake, record))

	diagnosis, err := Diagnose(context.Background(), fake, DiagnoseOptions{
		Client: diagnoseClient([]runtime.Object{liveProfileNode()}),
	})

	require.NoError(t, err)
	addresses := findCheck(t, diagnosis, "node addresses")
	require.Equal(t, SeverityWarn, addresses.Severity)
	require.Contains(t, addresses.Detail, "no cluster address is declared")
	require.Contains(t, addresses.Detail, "10.0.1.2 (enp7s0)")
}

// The shape this whole declaration exists for: a node advertising its
// public address while a private network sits unused.
func TestDiagnoseWarnsOnUnusedPrivateNetwork(t *testing.T) {
	t.Parallel()
	fake := multiHomedHost(t, "203.0.113.7")
	record, err := LoadRecord(context.Background(), fake)
	require.NoError(t, err)
	record.Node.SetNetwork(NodeNetwork{ClusterIP: "203.0.113.7"})
	record.Coordinator.Endpoints = []string{"https://203.0.113.7:6444"}
	require.NoError(t, SaveRecord(context.Background(), fake, record))
	fake.FS[K3sServingCertPath] = servingCertificate(t, "203.0.113.7")

	diagnosis, err := Diagnose(context.Background(), fake, DiagnoseOptions{
		Client: diagnoseClient(
			[]runtime.Object{liveProfileNode(), healthyDeployment("skali-registry"),
				healthyDeployment("skalid"), healthyDeployment("skali-web")},
			cnpgCluster(1, 1, "Cluster in healthy state")),
	})

	require.NoError(t, err)
	addresses := findCheck(t, diagnosis, "node addresses")
	require.Equal(t, SeverityWarn, addresses.Severity)
	require.Contains(t, addresses.Detail, "the private network 10.0.1.2 (enp7s0) carries no cluster traffic")
	require.Zero(t, diagnosis.Fails(), "an unused private network is advice, not a failure")
}
