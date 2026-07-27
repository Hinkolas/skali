package installer

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/layout"
)

// Severity grades one diagnostic check.
type Severity int

const (
	SeverityOK Severity = iota
	// SeverityWarn adds a line but never fails the diagnosis.
	SeverityWarn
	SeverityFail
)

// Check is one diagnostic probe result; Sub carries pod-level drilldown
// lines under a failing component.
type Check struct {
	Name     string
	Severity Severity
	Detail   string
	Sub      []string
}

// Diagnosis is the full read-only report. It is built from host state and
// the Kubernetes API alone: never the Skali API, the product database, or
// the registry, so it stays available exactly when those are down.
type Diagnosis struct {
	Host        *Host
	Checks      []Check
	Suggestions []string
}

// Fails counts failing checks; zero means a healthy report (warns
// allowed).
func (d *Diagnosis) Fails() int {
	count := 0
	for _, check := range d.Checks {
		if check.Severity == SeverityFail {
			count++
		}
	}
	return count
}

// DiagnoseOptions parameterize Diagnose.
type DiagnoseOptions struct {
	// Client overrides the kube client; nil derives it from the runner.
	Client *kube.Client
}

// registriesState classifies the host's registries.yaml.
type registriesState int

const (
	registriesOK registriesState = iota
	registriesFileMissing
	registriesMirrorMissing
	registriesCredentialMissing
)

// RegistriesState reads the host's registries.yaml shape without
// mutating it.
func RegistriesState(ctx context.Context, runner host.Runner) registriesState {
	registries, err := runner.ReadFile(ctx, K3sRegistriesPath)
	if err != nil {
		return registriesFileMissing
	}
	if !strings.Contains(string(registries), bundle.RegistryInternalHost) {
		return registriesMirrorMissing
	}
	if registriesPullSecret(registries) == "" {
		return registriesCredentialMissing
	}
	return registriesOK
}

// Diagnose runs the probe ladder: host level always (that is what
// survives a dead API), Kubernetes level only when the API answers.
// Strictly read-only.
func Diagnose(ctx context.Context, runner host.Runner, opts DiagnoseOptions) (*Diagnosis, error) {
	detected, err := Detect(ctx, runner)
	if err != nil {
		return nil, err
	}
	diagnosis := &Diagnosis{Host: detected}
	suggest := func(action string) {
		for _, existing := range diagnosis.Suggestions {
			if existing == action {
				return
			}
		}
		diagnosis.Suggestions = append(diagnosis.Suggestions, action)
	}

	// Host level. Damaged-state problems become fail checks first.
	if detected.State == StateInterrupted && detected.Record != nil && detected.Record.Lifecycle != nil {
		lifecycle := detected.Record.Lifecycle
		detail := lifecycle.Status + " at phase " + lifecycle.Phase
		if lifecycle.LastError != "" {
			detail += ": " + lifecycle.LastError
		}
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "install transaction", Severity: SeverityFail, Detail: detail,
		})
		suggest("rerun skali cluster install/join with corrected inputs, or choose resume/edit from skali cluster")
		suggest("skali cluster repair")
		suggest("skali cluster uninstall --scope node")
	}
	for _, problem := range detected.Problems {
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "installation", Severity: SeverityFail, Detail: problem,
		})
		if detected.Record == nil {
			suggest("restore the saved installation record; skali cluster restore lists the required inputs")
		} else {
			suggest("skali cluster repair")
		}
	}
	if detected.State == StateOrphaned {
		suggest("run skali cluster to reconstruct, resume, or uninstall this fingerprinted older installation")
	}
	if detected.Record != nil && detected.Record.Reconciled() {
		if HostdPresent(ctx, runner) {
			diagnosis.Checks = append(diagnosis.Checks,
				Check{Name: "host agent binary", Detail: "installed"})
		} else {
			diagnosis.Checks = append(diagnosis.Checks, Check{
				Name: "host agent binary", Severity: SeverityFail,
				Detail: HostdBinaryPath + " is missing",
			})
			suggest("skali cluster repair")
		}
		if probeUnitActive(ctx, runner, HostdAgentUnit) {
			diagnosis.Checks = append(diagnosis.Checks,
				Check{Name: "host agent service", Detail: "active"})
		} else {
			diagnosis.Checks = append(diagnosis.Checks, Check{
				Name: "host agent service", Severity: SeverityFail,
				Detail: HostdAgentUnit + " is not active",
			})
			suggest("skali cluster repair")
		}
		if detected.Record.Node.Role == layout.RoleServer &&
			!detected.Record.EnrolledOnly() {
			if probeUnitActive(ctx, runner, HostdCoordinatorUnit) {
				diagnosis.Checks = append(diagnosis.Checks,
					Check{Name: "coordinator service", Detail: "active"})
			} else {
				diagnosis.Checks = append(diagnosis.Checks, Check{
					Name: "coordinator service", Severity: SeverityFail,
					Detail: HostdCoordinatorUnit + " is not active",
				})
				suggest("skali cluster repair")
			}
		}
		if detected.Record.EnrolledOnly() && detected.K3sVersion == "" {
			return diagnosis, nil
		}
	}

	role := layout.RoleServer
	unit := "k3s.service"
	if detected.Record != nil && detected.Record.Node.Role == layout.RoleAgent {
		role = layout.RoleAgent
		unit = "k3s-agent.service"
	}
	unitActive := probeUnitActive(ctx, runner, unit)
	if unitActive {
		diagnosis.Checks = append(diagnosis.Checks, Check{Name: "k3s service", Detail: "active"})
	} else {
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "k3s service", Severity: SeverityFail, Detail: unit + " is not active",
		})
		suggest("skali cluster repair")
	}

	switch RegistriesState(ctx, runner) {
	case registriesOK:
		diagnosis.Checks = append(diagnosis.Checks, Check{Name: "registry mirror", Detail: "configured"})
	case registriesFileMissing:
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "registry mirror", Severity: SeverityFail,
			Detail: K3sRegistriesPath + " is missing",
		})
		suggest("skali cluster repair")
	case registriesMirrorMissing:
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "registry mirror", Severity: SeverityFail,
			Detail: K3sRegistriesPath + " is missing the " + bundle.RegistryInternalHost + " mirror",
		})
		suggest("skali cluster repair")
	case registriesCredentialMissing:
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "registry mirror", Severity: SeverityFail,
			Detail: K3sRegistriesPath + " is missing the node pull credential",
		})
		suggest("skali cluster repair")
	}

	diagnoseNodeNetwork(ctx, runner, detected, diagnosis, suggest)

	if detected.K3sVersion != "" && detected.K3sVersion != K3sVersion {
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "k3s version", Severity: SeverityWarn,
			Detail: detected.K3sVersion + " (expected " + K3sVersion + ")",
		})
		suggest("skali cluster upgrade")
	}

	// Kubernetes level; agents have no API access by design.
	if role == layout.RoleAgent || !unitActive {
		return diagnosis, nil
	}
	client := opts.Client
	if client == nil {
		client, err = KubeClient(ctx, runner)
		if err != nil {
			diagnosis.Checks = append(diagnosis.Checks, Check{
				Name: "kubernetes api", Severity: SeverityFail, Detail: "unreachable: " + err.Error(),
			})
			suggest("skali cluster repair")
			return diagnosis, nil
		}
	}
	expectBundle := detected.Record == nil || detected.Record.Versions.Bundle != ""
	if !expectBundle {
		if published, err := InClusterRecord(ctx, client); err == nil && published != nil {
			expectBundle = published.Versions.Bundle != ""
		}
	}
	diagnoseKubernetes(ctx, client, diagnosis, suggest, expectBundle)
	return diagnosis, nil
}

// DiagnoseCluster runs only the Kubernetes-level checks against a client,
// for existing-cluster mode, where there is no host to probe. The
// suggested actions never name host-repair operations.
func DiagnoseCluster(ctx context.Context, client *kube.Client) *Diagnosis {
	diagnosis := &Diagnosis{}
	suggest := func(action string) {
		for _, existing := range diagnosis.Suggestions {
			if existing == action {
				return
			}
		}
		diagnosis.Suggestions = append(diagnosis.Suggestions, action)
	}
	diagnoseKubernetes(ctx, client, diagnosis, suggest, true)
	return diagnosis
}

// diagnoseKubernetes appends the cluster-level checks: node readiness, the
// bootstrap database, the registry and skalid deployments, and warn-only
// volume and certificate checks.
func diagnoseKubernetes(ctx context.Context, client *kube.Client, diagnosis *Diagnosis,
	suggest func(string), expectBundle bool) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	nodes, err := client.Clientset.CoreV1().Nodes().List(probeCtx, metav1.ListOptions{})
	cancel()
	if err != nil {
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "kubernetes api", Severity: SeverityFail, Detail: "unreachable",
		})
		suggest("skali cluster repair")
		return
	}
	diagnosis.Checks = append(diagnosis.Checks, Check{Name: "kubernetes api", Detail: "reachable"})

	ready, notReady := 0, []string{}
	for _, node := range nodes.Items {
		isReady := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				isReady = true
			}
		}
		if isReady {
			ready++
		} else {
			notReady = append(notReady, "node "+node.Name+" is not ready")
		}
	}
	sort.Strings(notReady)
	nodesCheck := Check{
		Name:   "nodes",
		Detail: fmt.Sprintf("%d/%d ready", ready, len(nodes.Items)),
		Sub:    notReady,
	}
	if len(notReady) > 0 {
		nodesCheck.Severity = SeverityFail
		suggest("skali cluster repair")
	}
	diagnosis.Checks = append(diagnosis.Checks, nodesCheck)

	if !expectBundle {
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "platform bundle", Detail: "not initialized yet (expected)",
		})
		return
	}

	databaseFailed := diagnoseDatabase(ctx, client, diagnosis, suggest)
	diagnoseDeployment(ctx, client, diagnosis, suggest,
		"managed registry", "skali-registry", "app.kubernetes.io/name=skali-registry", false)
	diagnoseDeployment(ctx, client, diagnosis, suggest,
		"skalid", "skalid", "app.kubernetes.io/name=skalid", databaseFailed)
	diagnoseVolumes(ctx, client, diagnosis)
	diagnoseCertificates(ctx, client, diagnosis)
}

// diagnoseNodeNetwork covers the failure that looks like nothing else: a
// multi-homed node whose addresses disagree. The advertised address, the
// certificate that must cover it, and the coordinator socket that must
// answer on it are three independent facts, and a join fails when any one
// of them is off.
func diagnoseNodeNetwork(ctx context.Context, runner host.Runner, detected *Host,
	diagnosis *Diagnosis, suggest func(string)) {
	if detected.Record == nil {
		return
	}
	record := detected.Record
	network := record.Node.Network()
	local, err := DetectHostAddresses(ctx, runner)
	if err != nil {
		return
	}
	switch {
	case network.ClusterIP == "" && len(local) > 1:
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "node addresses", Severity: SeverityWarn,
			Detail: "no cluster address is declared, so k3s picked the default route on a " +
				"multi-homed host; assigned addresses are " + describeAddresses(local),
		})
	case network.ClusterIP != "" && !slices.ContainsFunc(local, func(address HostAddress) bool {
		return address.IP == network.ClusterIP
	}):
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "node addresses", Severity: SeverityFail,
			Detail: "declared cluster address " + network.ClusterIP +
				" is not assigned to this host; assigned addresses are " + describeAddresses(local),
		})
	default:
		detail := "cluster address " + orAuto(network.ClusterIP)
		if len(network.PublicIPs) > 0 {
			detail += ", public " + strings.Join(network.PublicIPs, ", ")
		}
		check := Check{Name: "node addresses", Detail: detail}
		// Cluster traffic on the public interface while a private network
		// sits unused is legal, and almost never what the operator wanted.
		if unused := unusedPrivateAddresses(local, network); len(unused) > 0 {
			check.Severity = SeverityWarn
			check.Detail = detail + "; the private network " + strings.Join(unused, ", ") +
				" carries no cluster traffic"
		}
		diagnosis.Checks = append(diagnosis.Checks, check)
	}

	if record.Node.Role == layout.RoleServer && !record.EnrolledOnly() {
		diagnoseAPICertificate(ctx, runner, record, network, diagnosis, suggest)
		diagnoseCoordinatorEndpoints(ctx, runner, record, diagnosis, suggest)
	}
}

// diagnoseAPICertificate proves the API server certificate covers every
// address this node is reachable at. A missing name does not refuse the
// connection, it fails the TLS handshake at join time, which is the least
// obvious way for a multi-homed server to be broken.
func diagnoseAPICertificate(ctx context.Context, runner host.Runner, record *Record,
	network NodeNetwork, diagnosis *Diagnosis, suggest func(string)) {
	data, err := runner.ReadFile(ctx, K3sServingCertPath)
	if err != nil {
		return
	}
	covered, err := certificateNames(data)
	if err != nil {
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "api certificate", Severity: SeverityWarn,
			Detail: "unreadable: " + err.Error(),
		})
		return
	}
	var missing []string
	for _, name := range network.APIServerSANs() {
		if !slices.Contains(covered, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "api certificate", Detail: "covers " + strings.Join(covered, ", "),
		})
		return
	}
	diagnosis.Checks = append(diagnosis.Checks, Check{
		Name: "api certificate", Severity: SeverityFail,
		Detail: "does not cover " + strings.Join(missing, ", ") +
			"; joins and kubeconfigs through those addresses fail the TLS handshake",
	})
	suggest("skali cluster repair")
}

// diagnoseCoordinatorEndpoints proves the enrollment endpoints this node
// hands out are actually served here. An advertised address nothing binds
// is exactly what a joining node sees as a refused connection.
func diagnoseCoordinatorEndpoints(ctx context.Context, runner host.Runner, record *Record,
	diagnosis *Diagnosis, suggest func(string)) {
	if !record.Reconciled() || record.Coordinator == nil || len(record.Coordinator.Endpoints) == 0 {
		return
	}
	listening, err := ListeningAddresses(ctx, runner)
	if err != nil {
		return
	}
	var unserved []string
	for _, endpoint := range record.Coordinator.Endpoints {
		address, port, err := endpointHostPort(endpoint)
		if err != nil {
			continue
		}
		if !EndpointServed(listening, address, port) {
			unserved = append(unserved, endpoint)
		}
	}
	if len(unserved) == 0 {
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "coordinator endpoint", Detail: strings.Join(record.Coordinator.Endpoints, ", "),
		})
		return
	}
	diagnosis.Checks = append(diagnosis.Checks, Check{
		Name: "coordinator endpoint", Severity: SeverityFail,
		Detail: "nothing is listening on " + strings.Join(unserved, ", ") +
			"; a node joining through that address is refused",
	})
	suggest("skali cluster repair")
}

// unusedPrivateAddresses names private addresses this node has but does
// not advertise, and only when the address it does advertise is public.
func unusedPrivateAddresses(local []HostAddress, network NodeNetwork) []string {
	advertised := ""
	for _, address := range local {
		if address.IP == network.ClusterIP {
			advertised = address.IP
			if address.Private {
				return nil
			}
		}
	}
	if advertised == "" {
		return nil
	}
	var unused []string
	for _, address := range local {
		if address.Private && address.IP != network.ClusterIP {
			unused = append(unused, address.IP+" ("+address.Interface+")")
		}
	}
	return unused
}

func endpointHostPort(endpoint string) (address, port string, err error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", "", err
	}
	port = parsed.Port()
	if port == "" {
		port = "443"
	}
	return parsed.Hostname(), port, nil
}

// certificateNames lists the IP and DNS names one PEM certificate covers.
func certificateNames(data []byte) ([]string, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no certificate found")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	names := append([]string(nil), certificate.DNSNames...)
	for _, ip := range certificate.IPAddresses {
		names = append(names, ip.String())
	}
	return names, nil
}

func orAuto(value string) string {
	if value == "" {
		return "(k3s default)"
	}
	return value
}

func probeUnitActive(ctx context.Context, runner host.Runner, unit string) bool {
	result, err := runner.Run(ctx, host.Command{
		Name: "systemctl", Args: []string{"is-active", unit},
	})
	return err == nil && result.ExitCode == 0
}

// diagnoseDatabase checks the CNPG cluster and drills into its pods on
// failure; the return feeds skalid's causal hint.
func diagnoseDatabase(ctx context.Context, client *kube.Client, diagnosis *Diagnosis, suggest func(string)) bool {
	resource := client.Dynamic.Resource(schema.GroupVersionResource{
		Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters",
	}).Namespace(bundle.Namespace)
	cluster, err := resource.Get(ctx, "skali-db", metav1.GetOptions{})
	if err != nil {
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: "bootstrap database", Severity: SeverityFail, Detail: "not found",
		})
		suggest("skali cluster repair")
		return true
	}
	phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
	readyInstances, _, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	if phase == "Cluster in healthy state" && readyInstances >= instances {
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name:   "bootstrap database",
			Detail: fmt.Sprintf("healthy (%s)", bundle.TierFromInstances(int(instances))),
		})
		return false
	}
	check := Check{
		Name:     "bootstrap database",
		Severity: SeverityFail,
		Detail:   fmt.Sprintf("%d/%d instances ready", readyInstances, instances),
		Sub:      podIssues(ctx, client, "cnpg.io/cluster=skali-db", suggest),
	}
	diagnosis.Checks = append(diagnosis.Checks, check)
	suggest("skali cluster repair")
	return true
}

func diagnoseDeployment(ctx context.Context, client *kube.Client, diagnosis *Diagnosis, suggest func(string),
	label, name, selector string, databaseFailed bool) {
	deployment, err := client.Clientset.AppsV1().Deployments(bundle.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		diagnosis.Checks = append(diagnosis.Checks, Check{
			Name: label, Severity: SeverityFail, Detail: "not found",
		})
		suggest("skali cluster repair")
		return
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if deployment.Status.AvailableReplicas >= desired {
		diagnosis.Checks = append(diagnosis.Checks, Check{Name: label, Detail: "healthy"})
		return
	}
	sub := podIssues(ctx, client, selector, suggest)
	detail := fmt.Sprintf("%d/%d replicas available", deployment.Status.AvailableReplicas, desired)
	for _, line := range sub {
		if strings.Contains(line, "CrashLoopBackOff") {
			detail = "CrashLoopBackOff"
			if databaseFailed {
				detail += " (cannot reach its database)"
			}
			break
		}
	}
	diagnosis.Checks = append(diagnosis.Checks, Check{
		Name: label, Severity: SeverityFail, Detail: detail, Sub: sub,
	})
	suggest("skali cluster repair")
}

// podIssues renders per-pod drilldown lines for the labeled skali-system
// pods that are not running cleanly: the phase, a scheduling message for
// Pending pods (or the latest warning event), and waiting reasons.
func podIssues(ctx context.Context, client *kube.Client, selector string, suggest func(string)) []string {
	pods, err := client.Clientset.CoreV1().Pods(bundle.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil
	}
	var lines []string
	for _, pod := range pods.Items {
		issue := podIssue(ctx, client, pod, suggest)
		if issue != "" {
			lines = append(lines, issue)
		}
	}
	sort.Strings(lines)
	return lines
}

func podIssue(ctx context.Context, client *kube.Client, pod corev1.Pod, suggest func(string)) string {
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil && status.State.Waiting.Reason != "" &&
			status.State.Waiting.Reason != "ContainerCreating" {
			return fmt.Sprintf("pod %s/%s: %s", bundle.Namespace, pod.Name, status.State.Waiting.Reason)
		}
	}
	if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodSucceeded {
		return ""
	}
	line := fmt.Sprintf("pod %s/%s: %s", bundle.Namespace, pod.Name, pod.Status.Phase)
	if pod.Status.Phase == corev1.PodPending {
		message := pendingMessage(ctx, client, pod)
		if message != "" {
			line += "\n  " + message
			if strings.Contains(message, "storage") || strings.Contains(message, "volume") {
				suggest("free or expand storage on the named node, then: skali cluster repair")
			}
		}
	}
	return line
}

// pendingMessage explains a Pending pod: the PodScheduled condition
// message, else the latest warning event for the pod.
func pendingMessage(ctx context.Context, client *kube.Client, pod corev1.Pod) string {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status != corev1.ConditionTrue &&
			condition.Message != "" {
			return condition.Message
		}
	}
	events, err := client.Clientset.CoreV1().Events(bundle.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + pod.Name,
	})
	if err != nil {
		return ""
	}
	message := ""
	for _, event := range events.Items {
		if event.Type == corev1.EventTypeWarning && event.Message != "" {
			message = event.Message
		}
	}
	return message
}

// diagnoseVolumes warns on pending skali-system claims; warns never flip
// the exit code.
func diagnoseVolumes(ctx context.Context, client *kube.Client, diagnosis *Diagnosis) {
	claims, err := client.Clientset.CoreV1().PersistentVolumeClaims(bundle.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	for _, claim := range claims.Items {
		if claim.Status.Phase == corev1.ClaimPending {
			diagnosis.Checks = append(diagnosis.Checks, Check{
				Name: "volume", Severity: SeverityWarn,
				Detail: "claim " + claim.Name + " is pending (unbound volume)",
			})
		}
	}
}

// diagnoseCertificates warns on certificates that are not ready with a
// terminal-looking reason; pending ACME issuance stays silent because
// converge never waits on TLS either.
func diagnoseCertificates(ctx context.Context, client *kube.Client, diagnosis *Diagnosis) {
	resource := client.Dynamic.Resource(schema.GroupVersionResource{
		Group: "cert-manager.io", Version: "v1", Resource: "certificates",
	}).Namespace(bundle.Namespace)
	certificates, err := resource.List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	for _, certificate := range certificates.Items {
		conditions, _, _ := unstructured.NestedSlice(certificate.Object, "status", "conditions")
		for _, entry := range conditions {
			condition, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if condition["type"] == "Ready" && condition["status"] == "False" {
				reason, _ := condition["reason"].(string)
				if reason == "" || reason == "Pending" || reason == "DoesNotExist" {
					continue
				}
				diagnosis.Checks = append(diagnosis.Checks, Check{
					Name: "certificate", Severity: SeverityWarn,
					Detail: certificate.GetName() + " is not ready (" + reason + ")",
				})
			}
		}
	}
}
