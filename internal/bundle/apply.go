package bundle

import (
	"context"
	"fmt"
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
		if _, err := a.Client.ApplyAs(ctx, object, kube.FieldManagerInstaller, true); err != nil {
			return fmt.Errorf("bundle: apply %s %s: %w", object.GetKind(), object.GetName(), err)
		}
	}
	return nil
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

// WaitDeploymentReady blocks until the deployment has at least one ready
// replica.
func (a *Applier) WaitDeploymentReady(ctx context.Context, namespace, name string) error {
	return a.wait(ctx, "deployment "+name, func(ctx context.Context) (bool, error) {
		deployment, err := a.Client.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return deployment.Status.ReadyReplicas > 0, nil
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

// WaitClusterReady blocks until the CNPG cluster reports a healthy phase.
func (a *Applier) WaitClusterReady(ctx context.Context, namespace, name string) error {
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
		return phase == "Cluster in healthy state" && ready > 0, nil
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
