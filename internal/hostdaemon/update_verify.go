package hostdaemon

import (
	"context"
	"fmt"
	"time"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/version"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func verifyReleaseReports(state *clusterstate.State, target clusterstate.Revision, k3s string, now time.Time) error {
	for _, desired := range clusterstate.SortedRevisionNodes(target.Nodes) {
		node := state.Nodes[desired.ID]
		if node.Phase != clusterstate.NodePhaseActive || node.LastSeen.IsZero() || now.Sub(node.LastSeen) > clusterstate.HeartbeatWindow {
			return fmt.Errorf("waiting for a healthy report from %s", desired.Name)
		}
		if node.AgentVersion != target.Platform.Version || node.K3sVersion != k3s {
			return fmt.Errorf("node %s reports hostd %s / Kubernetes %s; expected %s / %s", desired.Name, node.AgentVersion, node.K3sVersion, target.Platform.Version, k3s)
		}
		if desired.Role == "server" {
			coordinator := state.Updates.Coordinators[desired.ID]
			if coordinator.Version != target.Platform.Version || coordinator.LastSeen.IsZero() || now.Sub(coordinator.LastSeen) > clusterstate.HeartbeatWindow {
				return fmt.Errorf("waiting for coordinator %s to report release %s", desired.Name, target.Platform.Version)
			}
		}
	}
	return nil
}

func (d *CoordinatorDaemon) verifyRelease(ctx context.Context, state *clusterstate.State, target clusterstate.Revision) error {
	// Preflight persists the pin so a controller handoff does not require
	// the release server to stay available after all assets are installed.
	k3s := state.Updates.Operations[state.CurrentOperation].K3s
	if k3s == "" {
		metadata, err := d.releaseMetadata(ctx, target.Platform.Version)
		if err != nil {
			return err
		}
		if metadata == nil || metadata.K3s == "" {
			return fmt.Errorf("release %s has no Kubernetes version metadata", target.Platform.Version)
		}
		k3s = metadata.K3s
	}
	if err := verifyReleaseReports(state, target, k3s, time.Now()); err != nil {
		return err
	}
	client, err := installer.KubeClient(ctx, d.Runner)
	if err != nil {
		return err
	}
	for _, node := range target.Nodes {
		observed, err := client.Clientset.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if observed.Status.NodeInfo.KubeletVersion != k3s {
			return fmt.Errorf("node %s has not reached Kubernetes %s", node.Name, k3s)
		}
	}
	deployment, err := client.Clientset.AppsV1().Deployments(bundle.Namespace).Get(ctx, "skalid", metav1.GetOptions{})
	if err != nil {
		return err
	}
	return verifyReleaseDeployment(deployment, target.Platform.Version)
}

func verifyReleaseDeployment(deployment *appsv1.Deployment, target string) error {
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if desired == 0 || deployment.Status.ObservedGeneration < deployment.Generation ||
		deployment.Status.UpdatedReplicas != desired || deployment.Status.ReadyReplicas != desired ||
		deployment.Status.Replicas != desired || deployment.Status.AvailableReplicas != desired {
		return fmt.Errorf("waiting for the skalid rollout to finish")
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != "skalid" {
			continue
		}
		running, ok := version.PublishedSkalidVersion(container.Image)
		if !ok || running != target {
			return fmt.Errorf("skalid image %s does not match target %s", container.Image, target)
		}
		return nil
	}
	return fmt.Errorf("skalid deployment has no skalid container")
}
