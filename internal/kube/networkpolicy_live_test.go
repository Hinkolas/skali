package kube_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/Hinkolas/skali/internal/kubetest"
)

const (
	targetImage = "traefik/whoami:v1.10.2"
	probeImage  = "busybox:1.37"
	// roleLabel marks the test namespaces for namespaceSelector peers. It is
	// deliberately not a skali.dev label: nothing managed may select these
	// namespaces.
	roleLabel = "skali-test/role"
)

// Live: k3s ships flannel, which enforces nothing, plus an embedded network
// policy controller that does. The object-store fence and per-environment
// isolation are NetworkPolicies, so the pinned k3s must actually block
// traffic for the three shapes they use: a default-deny on ingress, an
// ingress allowance keyed on a namespace label, and a default-deny on egress
// with the DNS allowance a working pod needs. Policies are created straight
// through the clientset: the test namespaces carry no skali.dev ownership.
func TestLiveNetworkPolicyEnforced(t *testing.T) {
	clientset := kubetest.Clientset(t)

	server := labeledNamespace(t, clientset, "server")
	other := labeledNamespace(t, clientset, "other")
	stranger := kubetest.Namespace(t, clientset)

	startTarget(t, clientset, server)
	url := fmt.Sprintf("http://target.%s.svc.cluster.local", server)

	// Baseline: with no policy in place every pod reaches every service,
	// which also proves the probe itself before any denial is asserted.
	// Service endpoints and cluster DNS can lag a freshly created cluster,
	// so the baseline waits like every later step.
	eventuallyProbe(t, clientset, other, url, true, "without policies the probe must reach the target")

	// A default-deny on ingress closes the target to everyone, including
	// pods of its own namespace: the environment policy must therefore
	// carry an explicit intra-environment allowance.
	createPolicy(t, clientset, server, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny-ingress"},
		Spec:       networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}},
	})
	eventuallyProbe(t, clientset, other, url, false, "ingress default-deny must block another namespace")
	require.False(t, probe(t, clientset, server, url),
		"ingress default-deny must block the target's own namespace too")

	// A namespaceSelector allowance admits exactly the labelled namespace:
	// the shape that lets platform ports admit only claim holders.
	createPolicy(t, clientset, server, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-other"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "target"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From:  []networkingv1.NetworkPolicyPeer{{NamespaceSelector: roleSelector("other")}},
				Ports: []networkingv1.NetworkPolicyPort{tcpPort(80)},
			}},
		},
	})
	eventuallyProbe(t, clientset, other, url, true, "the namespaceSelector allowance must admit the labelled namespace")
	require.False(t, probe(t, clientset, stranger, url),
		"an unlabelled namespace must stay blocked")

	// A default-deny on egress cuts the client off even where ingress is
	// allowed; DNS to kube-system and the target port are what it takes to
	// get the same probe working again.
	createPolicy(t, clientset, other, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny-egress"},
		Spec:       networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}},
	})
	eventuallyProbe(t, clientset, other, url, false, "egress default-deny must block the client")
	createPolicy(t, clientset, other, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-egress"},
		Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
					}}},
					Ports: []networkingv1.NetworkPolicyPort{udpPort(53), tcpPort(53)},
				},
				{
					To:    []networkingv1.NetworkPolicyPeer{{NamespaceSelector: roleSelector("server")}},
					Ports: []networkingv1.NetworkPolicyPort{tcpPort(80)},
				},
			},
		},
	})
	eventuallyProbe(t, clientset, other, url, true, "DNS plus the target port must restore the client")
}

// labeledNamespace creates a test namespace carrying roleLabel=role.
func labeledNamespace(t *testing.T, clientset kubernetes.Interface, role string) string {
	t.Helper()
	name := kubetest.Namespace(t, clientset)
	patch := fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, roleLabel, role)
	_, err := clientset.CoreV1().Namespaces().Patch(context.Background(), name,
		types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	require.NoError(t, err, "label namespace %s", name)
	return name
}

func roleSelector(role string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{roleLabel: role}}
}

func tcpPort(port int32) networkingv1.NetworkPolicyPort {
	protocol, number := corev1.ProtocolTCP, intstr.FromInt32(port)
	return networkingv1.NetworkPolicyPort{Protocol: &protocol, Port: &number}
}

func udpPort(port int32) networkingv1.NetworkPolicyPort {
	protocol, number := corev1.ProtocolUDP, intstr.FromInt32(port)
	return networkingv1.NetworkPolicyPort{Protocol: &protocol, Port: &number}
}

func createPolicy(t *testing.T, clientset kubernetes.Interface, namespace string, policy *networkingv1.NetworkPolicy) {
	t.Helper()
	_, err := clientset.NetworkingV1().NetworkPolicies(namespace).Create(context.Background(), policy, metav1.CreateOptions{})
	require.NoError(t, err, "create policy %s/%s", namespace, policy.Name)
}

// startTarget runs the whoami pod behind a ClusterIP Service named target
// and waits for it to be ready.
func startTarget(t *testing.T, clientset kubernetes.Interface, namespace string) {
	t.Helper()
	ctx := context.Background()
	labels := map[string]string{"app": "target"}
	_, err := clientset.CoreV1().Pods(namespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Labels: labels},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "target",
			Image: targetImage,
			Ports: []corev1.ContainerPort{{ContainerPort: 80}},
		}}},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "create target pod")
	_, err = clientset.CoreV1().Services(namespace).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "target"},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(80)}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "create target service")

	deadline := time.Now().Add(2 * time.Minute)
	for {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, "target", metav1.GetOptions{})
		require.NoError(t, err, "get target pod")
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("target pod never became ready (phase %s)", pod.Status.Phase)
		}
		time.Sleep(2 * time.Second)
	}
}

// probe runs a one-shot busybox pod in namespace that fetches url with a
// short timeout and reports whether it succeeded. A fresh pod per probe
// keeps every attempt independent of connection state.
func probe(t *testing.T, clientset kubernetes.Interface, namespace, url string) bool {
	t.Helper()
	ctx := context.Background()
	name := "probe-" + uuid.NewString()[:8]
	_, err := clientset.CoreV1().Pods(namespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app": "probe"}},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "probe",
				Image:   probeImage,
				Command: []string{"wget", "-q", "-T", "3", "-O", "/dev/null", url},
			}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "create probe pod")
	defer func() {
		_ = clientset.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	}()

	deadline := time.Now().Add(90 * time.Second)
	for {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
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
// controller programs rules asynchronously after a policy is created.
func eventuallyProbe(t *testing.T, clientset kubernetes.Interface, namespace, url string, want bool, why string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if probe(t, clientset, namespace, url) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: probe from %s still reports reachable=%v", why, namespace, !want)
		}
		time.Sleep(2 * time.Second)
	}
}
