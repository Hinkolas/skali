package reconcile

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/Hinkolas/skali/internal/edge"
	"github.com/Hinkolas/skali/internal/kube"
	rendering "github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/kubetest"
	"github.com/Hinkolas/skali/internal/platform"
)

const probeImage = "busybox:1.37"

// isolationManifest is liveManifest's web application with a readiness
// probe (the kubelet must still reach the pod under the policy) and a
// plain-HTTP route (the edge must still reach it).
func isolationManifest(project, domain string) string {
	return "name: " + project + "\napplications:\n  web:\n" +
		"    image: traefik/whoami:v1.10.2\n" +
		"    ports:\n      http:\n        port: 80\n        protocol: http\n" +
		"    routes:\n      public:\n        domain: " + domain + "\n        port: http\n        tls: disabled\n" +
		"    health:\n      readiness:\n        http:\n          port: http\n          path: /health\n"
}

// Live: an environment is isolated on the network by default. Its pods are
// reached by its own pods and by the edge (a route through the real
// Traefik serves), and by nothing else: not another environment of the
// same installation, not an unrelated namespace, not an unlabelled pod in
// the edge's namespace. The kubelet readiness probe still passes under the
// policy, and a policy deleted out of band is restored by the next pass.
// Requires TEST_KUBECONFIG and TEST_DATABASE_URL.
func TestLiveEnvironmentIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	config := kubetest.Config(t)
	client, err := kube.NewFromConfig(config)
	require.NoError(t, err)

	holder := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute}, config)
	other := newLiveFixture(t, Config{RolloutDeadline: 5 * time.Minute}, config)
	holder.start(t)
	other.start(t)
	domain := holder.projectName + ".test"
	holderRun := holder.deployManifest(t, isolationManifest(holder.projectName, domain))
	otherRun := other.deployManifest(t, liveManifest(other.projectName, 1, false))
	holder.waitActive(t, holderRun.RevisionID, 3*time.Minute)
	other.waitActive(t, otherRun.RevisionID, 3*time.Minute)

	// Activation needed the Deployment ready, and ready came through the
	// rendered HTTP readiness probe: the kubelet reaches the pod under the
	// policy that was applied before the Deployment.
	deployment := holder.webDeployment(t)
	require.NotNil(t, deployment.Spec.Template.Spec.Containers[0].ReadinessProbe, "the manifest's readiness probe is rendered")
	require.Equal(t, int32(1), deployment.Status.ReadyReplicas)

	clientset := holder.clientset
	for _, namespace := range []string{holder.namespace, other.namespace} {
		policy, err := clientset.NetworkingV1().NetworkPolicies(namespace).Get(ctx, rendering.EnvironmentPolicyName, metav1.GetOptions{})
		require.NoError(t, err, "every environment namespace carries the isolation")
		require.Equal(t, rendering.RenderEnvironmentPolicy("", "", "").Spec.PolicyTypes, policy.Spec.PolicyTypes)
		require.Len(t, policy.Spec.Ingress, 2)
	}

	web := probeTarget{host: rendering.ApplicationName(holder.projectName, "web") + "." + holder.namespace + ".svc.cluster.local", port: 80}
	eventuallyReach(t, clientset, holder.namespace, nil, web, true, "a pod of the environment reaches its sibling service")
	eventuallyReach(t, clientset, other.namespace, nil, web, false, "another environment is denied")
	eventuallyReach(t, clientset, kubetest.Namespace(t, clientset), nil, web, false, "an unrelated namespace is denied")
	edgeLabels := map[string]string{platform.EdgePodLabel: platform.EdgePodName}
	eventuallyReach(t, clientset, platform.EdgeNamespace, edgeLabels, web, true, "the edge is admitted by namespace and pod label")
	eventuallyReach(t, clientset, platform.EdgeNamespace, nil, web, false, "an unlabelled pod in the edge's namespace is denied")

	// The production path: the route serves through the real Traefik pod,
	// whose connection to the application pod is what the edge rule admits.
	require.Eventually(t, func() bool {
		routes, err := client.Dynamic.Resource(edge.IngressRouteGVR).Namespace(holder.namespace).List(ctx, metav1.ListOptions{})
		return err == nil && len(routes.Items) > 0
	}, time.Minute, 2*time.Second, "the route renders an IngressRoute")
	viaEdge := []string{"wget", "-q", "-T", "3", "-O", "/dev/null", "--header", "Host: " + domain,
		"http://traefik." + platform.EdgeNamespace + ".svc.cluster.local/"}
	eventuallyProbe(t, clientset, other.namespace, nil, viaEdge, true, "the route serves through the edge")

	// Drift: the policy is applied on every pass, so an out-of-band delete
	// is undone by the next one.
	require.NoError(t, clientset.NetworkingV1().NetworkPolicies(holder.namespace).Delete(ctx, rendering.EnvironmentPolicyName, metav1.DeleteOptions{}))
	holder.kernel.Enqueue(holder.environmentID)
	require.Eventually(t, func() bool {
		_, err := clientset.NetworkingV1().NetworkPolicies(holder.namespace).Get(ctx, rendering.EnvironmentPolicyName, metav1.GetOptions{})
		return err == nil
	}, time.Minute, time.Second, "the next pass restores a deleted isolation policy")
	eventuallyReach(t, clientset, other.namespace, nil, web, false, "the restored policy denies again")
}

type probeTarget struct {
	host string
	port int
}

// probe runs a one-shot busybox pod in namespace (with the given labels)
// executing command, and reports whether it exited zero. A fresh pod per
// probe keeps attempts independent.
func probe(t *testing.T, clientset kubernetes.Interface, namespace string, labels map[string]string, command []string) bool {
	t.Helper()
	ctx := context.Background()
	name := "probe-" + uuid.NewString()[:8]
	pods := clientset.CoreV1().Pods(namespace)
	_, err := pods.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: "probe", Image: probeImage, Command: command}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "create probe pod in %s", namespace)
	defer func() { _ = pods.Delete(ctx, name, metav1.DeleteOptions{}) }()

	deadline := time.Now().Add(90 * time.Second)
	for {
		pod, err := pods.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			t.Fatalf("probe %s/%s vanished", namespace, name)
		}
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

// eventuallyProbe repeats probe until it reports want: the policy
// controller programs rules asynchronously.
func eventuallyProbe(t *testing.T, clientset kubernetes.Interface, namespace string, labels map[string]string, command []string, want bool, why string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if probe(t, clientset, namespace, labels, command) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: probe from %s still reports success=%v", why, namespace, !want)
		}
		time.Sleep(2 * time.Second)
	}
}

// eventuallyReach is eventuallyProbe for a plain TCP connect.
func eventuallyReach(t *testing.T, clientset kubernetes.Interface, namespace string, labels map[string]string, to probeTarget, want bool, why string) {
	t.Helper()
	eventuallyProbe(t, clientset, namespace, labels, []string{"nc", "-z", "-w", "3", to.host, strconv.Itoa(to.port)}, want, why)
}
