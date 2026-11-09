package bundle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Hinkolas/skali/internal/kube"
)

// edgeWaitInterval paces WaitEdgeTraefik's polls; a variable so tests can
// shorten it.
var edgeWaitInterval = 2 * time.Second

// WaitEdgeTraefik waits until the k3s Traefik DaemonSet runs the profile's
// edge settings on every node it is scheduled to. Converge only hands the
// chart overlay to k3s's helm-controller, which re-renders Traefik in a job
// of its own afterwards; an operator told to enable PROXY protocol on a
// load balancer once init succeeds must not race that job, or every
// connection the balancer forwards breaks until it lands.
func WaitEdgeTraefik(ctx context.Context, client *kube.Client, profile Profile, timeout time.Duration) error {
	var trusted []string
	if profile.Production != nil {
		trusted = profile.Production.ProxyProtocolTrustedIPs
	}
	deadline := time.Now().Add(timeout)
	for {
		daemonSet, err := client.Clientset.AppsV1().DaemonSets("kube-system").Get(ctx, "traefik", metav1.GetOptions{})
		var pending string
		switch {
		case apierrors.IsNotFound(err):
			pending = "the traefik DaemonSet does not exist yet"
		case err != nil:
			pending = err.Error()
		default:
			pending = edgeTraefikPending(daemonSet, trusted)
		}
		if pending == "" {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the Traefik edge has not rolled out its settings after %s (%s); "+
				"check the helm-install-traefik job in kube-system, and do not enable PROXY protocol "+
				"on a load balancer until it has", timeout, pending)
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), errors.New(pending))
		case <-time.After(edgeWaitInterval):
		}
	}
}

// edgeTraefikPending explains what the DaemonSet still lacks, or returns
// "" once it carries exactly the expected PROXY protocol trust on both
// entrypoints and every scheduled pod runs that template.
func edgeTraefikPending(daemonSet *appsv1.DaemonSet, trusted []string) string {
	var args []string
	for _, container := range daemonSet.Spec.Template.Spec.Containers {
		if container.Name == "traefik" {
			args = container.Args
		}
	}
	for _, entryPoint := range []string{"web", "websecure"} {
		prefix := "--entryPoints." + entryPoint + ".proxyProtocol.trustedIPs="
		index := slices.IndexFunc(args, func(arg string) bool { return strings.HasPrefix(arg, prefix) })
		switch {
		case len(trusted) == 0 && index >= 0:
			return "traefik still trusts PROXY protocol on " + entryPoint
		case len(trusted) > 0 && (index < 0 || args[index] != prefix+strings.Join(trusted, ",")):
			return "traefik does not yet trust " + strings.Join(trusted, ", ") + " for PROXY protocol on " + entryPoint
		}
	}
	status := daemonSet.Status
	switch {
	case status.ObservedGeneration < daemonSet.Generation:
		return "the traefik DaemonSet change is not observed yet"
	case status.DesiredNumberScheduled == 0:
		return "no edge node runs traefik yet"
	case status.UpdatedNumberScheduled < status.DesiredNumberScheduled ||
		status.NumberAvailable < status.DesiredNumberScheduled:
		return fmt.Sprintf("traefik is rolling out (%d of %d edge nodes updated, %d available)",
			status.UpdatedNumberScheduled, status.DesiredNumberScheduled, status.NumberAvailable)
	}
	return ""
}
