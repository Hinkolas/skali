package substrate

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/platform"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/testdb"
)

const probeImage = "busybox:1.37"

// TestLivePlatformPortsAdmitClaimHolders proves, against a real store and
// pool on the pinned k3s, that the platform ports are closed to everything
// but their claim holders: the environment holding a bucket and a database
// reaches the S3 gateway and the pool, another environment reaches
// neither, the fixed peers (the edge, the skalid pod) are admitted by their
// namespace and pod labels, the operator keeps the pool healthy through the
// policy, a local platform's host still reaches both through the loopback
// NodePorts, and releasing the claims closes the ports to the former
// holder. The policy that once opened the S3 port to every pod is swept.
// Requires TEST_KUBECONFIG and TEST_DATABASE_URL.
func TestLivePlatformPortsAdmitClaimHolders(t *testing.T) {
	defer livePhase(t, "scenario")()
	config := kubetest.Config(t)
	pool := testdb.New(t)
	ctx := context.Background()

	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)
	cleanupPlatform(t, client)
	installOperator(t, client)

	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	projects := project.New(st)

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	proj, err := projects.Create(ctx, "demo"+suffix, "", uuid.Nil)
	require.NoError(t, err)
	holder, err := projects.CreateEnvironment(ctx, proj.ID, "holder", project.EnvironmentOptions{})
	require.NoError(t, err)
	bystander, err := projects.CreateEnvironment(ctx, proj.ID, "bystander", project.EnvironmentOptions{})
	require.NoError(t, err)

	controller := New(Deps{
		DB:       dbSvc,
		Cluster:  KubeCluster{Client: client},
		Observed: observe.NewStore(nil),
		Seaweed:  seaweed.NewClient(client, Namespace),
		Enqueue:  func(uuid.UUID) {},
	}, Config{Managed: false})

	for _, env := range []*store.Environment{holder, bystander} {
		namespace := kubernetes.RenderNamespace(proj.Name, env.Name, env.ID.String())
		t.Cleanup(func() { deleteNamespace(t, client, namespace.Name) })
		_, err = client.Apply(ctx, namespace, false)
		require.NoError(t, err)
	}
	holderNS := kubernetes.NamespaceName(holder.ID.String())
	bystanderNS := kubernetes.NamespaceName(bystander.ID.String())

	// An upgraded installation carries the policy that opened the S3 port
	// to every pod; the first store pass must sweep it, or the fence below
	// would be void (policies union).
	require.NoError(t, controller.ensureNamespace(ctx))
	s3Port, tcp := intstr.FromInt32(platform.S3Port), corev1.ProtocolTCP
	_, err = client.Clientset.NetworkingV1().NetworkPolicies(Namespace).Create(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: seaweed.LegacyS3OpenPolicy},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{seaweed.SystemLabel: "object-storage"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{{Port: &s3Port, Protocol: &tcp}}}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	// The holder claims a bucket and a database; the bystander claims nothing.
	driveLiveBucket(t, controller, dbSvc, proj, holder, "files")
	owner := dbstore.ServiceOwner(proj.ID, holder.ID, proj.Name, holder.Name, "data")
	created, err := dbSvc.EnsureClaim(ctx, owner, dbstore.ClaimSpec{
		Engine: "postgres", Major: 17, Isolation: "project", Availability: "single",
	})
	require.NoError(t, err)
	driveClaim(t, controller, dbSvc, created.ID)
	tenant, err := dbSvc.LiveTenant(ctx, created.ID)
	require.NoError(t, err)
	dbPool, err := dbSvc.GetCluster(ctx, tenant.ClusterID)
	require.NoError(t, err)

	s3 := target{host: platform.S3Service + "." + Namespace + ".svc.cluster.local", port: platform.S3Port}
	postgres := target{host: tenant.Host, port: int(tenant.Port)}

	_, err = client.Clientset.NetworkingV1().NetworkPolicies(Namespace).Get(ctx, seaweed.LegacyS3OpenPolicy, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "the legacy open policy must be swept on the first store pass")

	// 1-2: holders reach, bystanders do not.
	eventuallyReach(t, client, holderNS, nil, s3, true, "the bucket holder must reach the S3 gateway")
	eventuallyReach(t, client, holderNS, nil, postgres, true, "the database holder must reach its pool")
	eventuallyReach(t, client, bystanderNS, nil, s3, false, "an environment without a bucket must not reach the S3 gateway")
	eventuallyReach(t, client, bystanderNS, nil, postgres, false, "an environment without a database must not reach the pool")

	// 3: the fixed peers are admitted by namespace and pod label together.
	traefik := map[string]string{"app.kubernetes.io/name": "traefik"}
	eventuallyReach(t, client, "kube-system", traefik, s3, true, "the edge must reach the S3 gateway for bucket routes")
	require.False(t, reach(t, client, "kube-system", nil, s3), "an unlabelled kube-system pod is not the edge")
	ensureNamespaceForTest(t, client, bundle.Namespace)
	skalid := map[string]string{"app.kubernetes.io/name": "skalid"}
	eventuallyReach(t, client, bundle.Namespace, skalid, s3, true, "the skalid pod must reach the S3 gateway")

	// 4: the operator and the instances keep working through the policy.
	requireEventually(t, 2*time.Minute, func() bool {
		ready, _, err := controller.poolReady(ctx, *dbPool)
		return err == nil && ready
	}, "the pool must stay healthy behind its access policy")
	applied, _, err := controller.databaseApplied(ctx, *tenant)
	require.NoError(t, err)
	require.True(t, applied, "the instance manager must still reconcile the tenant database")

	// 5: the local platform's host reaches both through the loopback
	// NodePorts. Host traffic enters the node through Docker's port
	// publish, so only a client outside the node on the cluster's Docker
	// network exercises the same path; a hostNetwork pod would ride the
	// node-local exemption and prove nothing.
	t.Run("host", func(t *testing.T) {
		network := os.Getenv("TEST_K3D_NETWORK")
		if network == "" {
			network = "k3d-skali-test"
		}
		if _, err := exec.LookPath("docker"); err != nil {
			t.Skip("docker not on PATH")
		}
		if err := exec.Command("docker", "network", "inspect", network).Run(); err != nil {
			t.Skipf("docker network %s not found (set TEST_K3D_NETWORK)", network)
		}
		require.NotNil(t, dbPool.NodePort, "a local pool carries a loopback NodePort")
		nodeIP := nodeInternalIP(t, client)
		for name, port := range map[string]int{"postgres": int(*dbPool.NodePort), "s3": bundle.S3NodePort} {
			out, err := exec.Command("docker", "run", "--rm", "--network", network, probeImage,
				"nc", "-z", "-w", "3", nodeIP, strconv.Itoa(port)).CombinedOutput()
			require.NoError(t, err, "host-side %s NodePort %d must stay reachable: %s", name, port, out)
		}
	})

	// 6: releasing the claims closes the ports to the former holder.
	_, err = dbSvc.ReleaseClaim(ctx, created.ID)
	require.NoError(t, err)
	driveLive(t, "database release", 3*time.Minute, func(ctx context.Context) (bool, error) {
		return databaseClaimPass(ctx, controller, dbSvc, created.ID, claim.PhaseReleased)
	})
	bucketClaim, err := dbSvc.LiveServiceBucketClaim(ctx, holder.ID, "files")
	require.NoError(t, err)
	_, err = dbSvc.ReleaseBucketClaim(ctx, bucketClaim.ID)
	require.NoError(t, err)
	driveLive(t, "bucket release", 3*time.Minute, func(ctx context.Context) (bool, error) {
		return bucketClaimPass(ctx, controller, dbSvc, bucketClaim.ID, claim.PhaseReleased, false)
	})
	eventuallyReach(t, client, holderNS, nil, s3, false, "a released bucket closes the S3 gateway to its environment")
	eventuallyReach(t, client, holderNS, nil, postgres, false, "a released database closes the pool to its environment")
}

type target struct {
	host string
	port int
}

// reach runs a one-shot pod in namespace (with the given labels) that opens
// a TCP connection to the target with a short timeout, and reports whether
// it succeeded. A fresh pod per probe keeps attempts independent.
func reach(t *testing.T, client *kube.Client, namespace string, labels map[string]string, to target) bool {
	t.Helper()
	ctx := context.Background()
	name := "probe-" + uuid.NewString()[:8]
	pods := client.Clientset.CoreV1().Pods(namespace)
	_, err := pods.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "probe",
				Image:   probeImage,
				Command: []string{"nc", "-z", "-w", "3", to.host, strconv.Itoa(to.port)},
			}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "create probe pod in %s", namespace)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := pods.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleanup probe %s/%s: %v", namespace, name, err)
		}
	}()

	deadline := time.Now().Add(90 * time.Second)
	for {
		pod, err := pods.Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err, "get probe pod")
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return true
		case corev1.PodFailed:
			return false
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe %s/%s never finished (phase %s)", namespace, name, pod.Status.Phase)
		}
		time.Sleep(time.Second)
	}
}

// eventuallyReach repeats reach until it reports want: the policy
// controller programs rules asynchronously.
func eventuallyReach(t *testing.T, client *kube.Client, namespace string, labels map[string]string, to target, want bool, why string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if reach(t, client, namespace, labels, to) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: probe from %s to %s:%d still reports reachable=%v", why, namespace, to.host, to.port, !want)
		}
		time.Sleep(2 * time.Second)
	}
}

// ensureNamespaceForTest creates a namespace the probes need and removes
// it afterwards only if the test created it.
func ensureNamespaceForTest(t *testing.T, client *kube.Client, name string) {
	t.Helper()
	ctx := context.Background()
	_, err := client.Clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return
	}
	require.NoError(t, err)
	t.Cleanup(func() { deleteNamespace(t, client, name) })
}

func nodeInternalIP(t *testing.T, client *kube.Client) string {
	t.Helper()
	nodes, err := client.Clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	for _, node := range nodes.Items {
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP {
				return address.Address
			}
		}
	}
	t.Fatal("no node reports an internal IP")
	return ""
}
