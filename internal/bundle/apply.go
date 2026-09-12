package bundle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Hinkolas/skali/internal/kube"
)

// Applier drives ordered bundle stages over server-side apply under the
// installer field manager, with readiness waits between stages.
type Applier struct {
	Client *kube.Client

	// PollInterval and per-wait timeouts; zero values pick defaults.
	PollInterval time.Duration
	WaitTimeout  time.Duration
}

func (a *Applier) poll() time.Duration {
	if a.PollInterval > 0 {
		return a.PollInterval
	}
	return 2 * time.Second
}

func (a *Applier) timeout() time.Duration {
	if a.WaitTimeout > 0 {
		return a.WaitTimeout
	}
	return 5 * time.Minute
}

// ApplyObjects server-side-applies objects in order under the installer
// manager. Conflicts are forced: the installer owns its bundle and a
// re-run must win over drift.
func (a *Applier) ApplyObjects(ctx context.Context, objects []unstructured.Unstructured) error {
	for index := range objects {
		object := &objects[index]
		if err := a.applyObject(ctx, object); err != nil {
			return fmt.Errorf("bundle: apply %s %s: %w", object.GetKind(), object.GetName(), err)
		}
	}
	return nil
}

// applyObject server-side-applies one object. A Job's pod template is
// immutable, so an upgrade that renders the bundle's job with a new image
// (skali-bootstrap-user carries the skalid image) cannot be applied over
// the finished job from the previous release: the API server rejects it
// as invalid. The bundle's jobs are idempotent by contract, so the
// finished job is deleted and the new one created in its place.
func (a *Applier) applyObject(ctx context.Context, object *unstructured.Unstructured) error {
	_, err := a.Client.ApplyAs(ctx, object, kube.FieldManagerInstaller, true)
	if err == nil || !jobTemplateConflict(object, err) {
		return err
	}
	if err := a.deleteJob(ctx, object.GetNamespace(), object.GetName()); err != nil {
		return err
	}
	_, err = a.Client.ApplyAs(ctx, object, kube.FieldManagerInstaller, true)
	return err
}

// jobTemplateConflict recognizes the API server refusing a changed pod
// template on an existing Job.
func jobTemplateConflict(object *unstructured.Unstructured, err error) bool {
	return object.GetKind() == "Job" && errors.IsInvalid(err) &&
		strings.Contains(err.Error(), "field is immutable")
}

// deleteJob removes a job and its pods and waits until the name is free
// again; a job that is already gone is fine.
func (a *Applier) deleteJob(ctx context.Context, namespace, name string) error {
	propagation := metav1.DeletePropagationBackground
	jobs := a.Client.Clientset.BatchV1().Jobs(namespace)
	if err := jobs.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("replace job: %w", err)
	}
	return a.wait(ctx, "job "+name+" removal", func(ctx context.Context) (bool, error) {
		_, err := jobs.Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

// ApplyObjectsRetry applies like ApplyObjects but retries the whole set
// until the deadline: webhook-validated objects race their operator's
// serving certs, and the retry absorbs the warm-up window.
func (a *Applier) ApplyObjectsRetry(ctx context.Context, objects []unstructured.Unstructured, deadline time.Duration) error {
	limit := time.Now().Add(deadline)
	for {
		err := a.ApplyObjects(ctx, objects)
		if err == nil {
			return nil
		}
		if time.Now().After(limit) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// ApplyManifest applies one multi-document YAML manifest (the vendored
// operators) and resets the REST mapper afterwards so kinds introduced by
// new CRDs resolve.
func (a *Applier) ApplyManifest(ctx context.Context, manifest []byte) error {
	objects, err := ParseManifest(manifest)
	if err != nil {
		return err
	}
	if err := a.ApplyObjects(ctx, objects); err != nil {
		return err
	}
	a.ResetMapper()
	return nil
}

// ResetMapper drops the cached REST mappings; required after installing
// CRDs.
func (a *Applier) ResetMapper() {
	if resettable, ok := a.Client.Mapper.(interface{ Reset() }); ok {
		resettable.Reset()
	}
}

// WaitDeploymentReady blocks until the deployment's rollout is complete:
// the controller observed the applied generation and every replica is
// updated and available. Anything weaker passes on the old pod while a
// rolling update is still replacing it.
func (a *Applier) WaitDeploymentReady(ctx context.Context, namespace, name string) error {
	return a.wait(ctx, "deployment "+name, func(ctx context.Context) (bool, error) {
		deployment, err := a.Client.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		desired := int32(1)
		if deployment.Spec.Replicas != nil {
			desired = *deployment.Spec.Replicas
		}
		status := deployment.Status
		return status.ObservedGeneration >= deployment.Generation &&
			status.UpdatedReplicas == desired &&
			status.Replicas == desired &&
			status.AvailableReplicas == desired, nil
	})
}

// WaitDaemonSetReady blocks until the daemon set's rollout is complete on
// every scheduled node. A daemon set with zero scheduled nodes never reads
// as ready: it would mean the selector matches no node, which is a
// placement bug, not a healthy rollout.
func (a *Applier) WaitDaemonSetReady(ctx context.Context, namespace, name string) error {
	return a.wait(ctx, "daemon set "+name, func(ctx context.Context) (bool, error) {
		set, err := a.Client.Clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		status := set.Status
		return status.ObservedGeneration >= set.Generation &&
			status.DesiredNumberScheduled > 0 &&
			status.UpdatedNumberScheduled == status.DesiredNumberScheduled &&
			status.NumberAvailable == status.DesiredNumberScheduled, nil
	})
}

// WaitJobComplete blocks until the job succeeded; a failed job errors with
// its terminal state.
func (a *Applier) WaitJobComplete(ctx context.Context, namespace, name string) error {
	return a.wait(ctx, "job "+name, func(ctx context.Context) (bool, error) {
		job, err := a.Client.Clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		if job.Status.Succeeded > 0 {
			return true, nil
		}
		for _, condition := range job.Status.Conditions {
			if condition.Type == "Failed" && condition.Status == "True" {
				return false, fmt.Errorf("bundle: job %s failed: %s", name, condition.Message)
			}
		}
		return false, nil
	})
}

// WaitClusterReady blocks until the CNPG cluster reports a healthy phase
// with at least minReady ready instances; tier-sized production clusters
// pass their instance count, the local profile passes 1.
func (a *Applier) WaitClusterReady(ctx context.Context, namespace, name string, minReady int) error {
	return a.wait(ctx, "database cluster "+name, func(ctx context.Context) (bool, error) {
		resource := a.Client.Dynamic.Resource(cnpgClusterResource()).Namespace(namespace)
		cluster, err := resource.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
		ready, _, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")
		return phase == "Cluster in healthy state" && ready >= int64(minReady), nil
	})
}

func cnpgClusterResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
}

func (a *Applier) wait(ctx context.Context, what string, probe func(context.Context) (bool, error)) error {
	waitCtx, cancel := context.WithTimeout(ctx, a.timeout())
	defer cancel()
	ticker := time.NewTicker(a.poll())
	defer ticker.Stop()
	for {
		done, err := probe(waitCtx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("bundle: timed out waiting for %s", what)
		case <-ticker.C:
		}
	}
}
