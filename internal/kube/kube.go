// Package kube constructs the cluster client and executes server-side
// apply, delete, and field-ownership operations for the reconciliation
// kernel. It works on runtime.Object plus ownership metadata and knows
// nothing about environments or revisions.
//
// Field-ownership contract: skalid applies with the stable field manager
// FieldManagerProject and force=false in steady state; a conflict is a bug,
// never something to force through. Applied configurations are the full
// intent: they never include status, server-populated metadata, or fields
// another controller legitimately owns (Deployment.spec.replicas while an
// autoscaler is active is the canonical case). Dropping a field from the
// applied configuration removes it via server-side apply; that is the prune
// semantics for fields. Ownership transfer during scale-mode transitions
// uses DisownFields (release without deleting the live value) and a single
// forced apply (retake with an explicit value).
package kube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

const (
	// FieldManagerProject owns every project-scoped object skalid applies.
	FieldManagerProject = "skalid-project"
	// FieldManagerPlatform owns skalid's platform resources (the shared
	// database and object-storage substrates in skali-platform).
	FieldManagerPlatform = "skalid-platform"
	// FieldManagerInstaller owns the installer-managed system bundle
	// (skali-system); skalid never reconciles or prunes under it.
	FieldManagerInstaller = "skali-installer"
)

// ErrNoCluster: no explicit kubeconfig was given and in-cluster
// configuration is unavailable. skalid runs API-only in that case.
var ErrNoCluster = errors.New("kube: no cluster configuration resolved")

type Client struct {
	Config    *rest.Config
	Clientset kubernetes.Interface
	Dynamic   dynamic.Interface
	Mapper    meta.RESTMapper

	// Lazily built service-proxy transport (proxy.go).
	proxyOnce sync.Once
	proxyHTTP *http.Client
	proxyBase *url.URL
	proxyErr  error
}

// New resolves cluster credentials. A set kubeconfigPath must load or the
// call fails; with it unset, in-cluster configuration is attempted and
// ErrNoCluster reports its absence. The ambient KUBECONFIG variable is
// deliberately never consulted.
func New(kubeconfigPath string) (*Client, error) {
	if kubeconfigPath != "" {
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("kube: load kubeconfig %s: %w", kubeconfigPath, err)
		}
		return NewFromConfig(config)
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, ErrNoCluster
	}
	return NewFromConfig(config)
}

// NewFromConfig builds the client set over an already-resolved rest.Config;
// tests use it to install counting or severable transports.
func NewFromConfig(config *rest.Config) (*Client, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("kube: build clientset: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("kube: build dynamic client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("kube: build discovery client: %w", err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))
	return &Client{
		Config:    config,
		Clientset: clientset,
		Dynamic:   dynamicClient,
		Mapper:    mapper,
	}, nil
}

// ObjectRef identifies one live object. UID, when set, preconditions
// deletion so a recreated namesake is never deleted by mistake.
type ObjectRef struct {
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
	UID       types.UID
}

func (r ObjectRef) String() string {
	if r.Namespace == "" {
		return r.GVK.Kind + "/" + r.Name
	}
	return r.GVK.Kind + "/" + r.Namespace + "/" + r.Name
}

type ApplyResult struct {
	// Changed reports whether the apply materially moved the object. For
	// spec-bearing kinds this compares metadata.generation, which only spec
	// writes advance; status churn by other controllers between the read and
	// the apply never counts. Generationless kinds (Secrets) fall back to
	// resourceVersion, which only skalid writes for objects it renders.
	Changed bool
	Live    *unstructured.Unstructured
}

// Apply server-side-applies one rendered object under FieldManagerProject.
func (c *Client) Apply(ctx context.Context, obj runtime.Object, force bool) (ApplyResult, error) {
	return c.ApplyAs(ctx, obj, FieldManagerProject, force)
}

// ApplyAs server-side-applies one rendered object under an explicit field
// manager; the installer bundle applies under FieldManagerInstaller.
func (c *Client) ApplyAs(ctx context.Context, obj runtime.Object, manager string, force bool) (ApplyResult, error) {
	applied, resource, err := c.prepare(obj)
	if err != nil {
		return ApplyResult{}, err
	}
	priorVersion := ""
	priorGeneration := int64(0)
	if live, err := resource.Get(ctx, applied.GetName(), metav1.GetOptions{}); err == nil {
		priorVersion = live.GetResourceVersion()
		priorGeneration = live.GetGeneration()
	} else if !apierrors.IsNotFound(err) {
		return ApplyResult{}, fmt.Errorf("kube: get %s before apply: %w", applied.GetName(), err)
	}
	data, err := applied.MarshalJSON()
	if err != nil {
		return ApplyResult{}, fmt.Errorf("kube: encode %s: %w", applied.GetName(), err)
	}
	result, err := resource.Patch(ctx, applied.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: manager,
		Force:        &force,
	})
	if err != nil {
		return ApplyResult{}, fmt.Errorf("kube: apply %s: %w", applied.GetName(), err)
	}
	changed := result.GetResourceVersion() != priorVersion
	if result.GetGeneration() > 0 {
		changed = result.GetGeneration() != priorGeneration
	}
	return ApplyResult{Changed: changed, Live: result}, nil
}

// Delete removes one object, reporting whether anything was deleted. A set
// UID preconditions the delete; NotFound counts as success because the
// desired state is absence.
func (c *Client) Delete(ctx context.Context, ref ObjectRef) (bool, error) {
	resource, err := c.resource(ref.GVK, ref.Namespace)
	if err != nil {
		return false, err
	}
	// Background propagation is explicit because batch/v1 Jobs still default
	// to orphaning their pods at the API level; every other managed kind
	// already cascades in the background, so this only pins the behavior.
	propagation := metav1.DeletePropagationBackground
	options := metav1.DeleteOptions{PropagationPolicy: &propagation}
	if ref.UID != "" {
		options.Preconditions = &metav1.Preconditions{UID: &ref.UID}
	}
	if err := resource.Delete(ctx, ref.Name, options); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("kube: delete %s: %w", ref, err)
	}
	return true, nil
}

// DisownFields releases ownership of the given FieldsV1 paths (for example
// "f:spec.f:replicas") from one manager's Apply entry without touching the
// live values: the managedFields entry is rewritten and submitted as a
// metadata-only update, which the API server explicitly permits. Absent
// entries or paths are a no-op, keeping the operation idempotent. The
// read-modify-write retries on conflict: any status writer bumping the
// resourceVersion between the read and the update is routine, not an error.
func (c *Client) DisownFields(ctx context.Context, ref ObjectRef, manager string, paths ...string) error {
	resource, err := c.resource(ref.GVK, ref.Namespace)
	if err != nil {
		return err
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		live, err := resource.Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("kube: get %s for disown: %w", ref, err)
		}
		rewritten, changed, err := RemoveOwnedFields(live.GetManagedFields(), manager, paths...)
		if err != nil {
			return fmt.Errorf("kube: rewrite managed fields of %s: %w", ref, err)
		}
		if !changed {
			return nil
		}
		live.SetManagedFields(rewritten)
		_, err = resource.Update(ctx, live, metav1.UpdateOptions{})
		return err
	})
	if err != nil && !apierrors.IsConflict(err) {
		return err
	}
	if err != nil {
		return fmt.Errorf("kube: update managed fields of %s: %w", ref, err)
	}
	return nil
}

// prepare converts a rendered object into its applied configuration:
// unstructured, without status or server-populated metadata.
func (c *Client) prepare(obj runtime.Object) (*unstructured.Unstructured, dynamic.ResourceInterface, error) {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, nil, fmt.Errorf("kube: convert to unstructured: %w", err)
	}
	applied := &unstructured.Unstructured{Object: content}
	StripServerFields(applied)
	gvk := applied.GroupVersionKind()
	if gvk.Empty() {
		return nil, nil, fmt.Errorf("kube: object %s has no TypeMeta", applied.GetName())
	}
	resource, err := c.resource(gvk, applied.GetNamespace())
	if err != nil {
		return nil, nil, err
	}
	return applied, resource, nil
}

func (c *Client) resource(gvk schema.GroupVersionKind, namespace string) (dynamic.ResourceInterface, error) {
	mapping, err := c.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if meta.IsNoMatchError(err) {
		// A CRD established after the discovery cache warmed (the Traefik
		// chart racing skalid on a fresh cluster) stays a cache miss until
		// the mapper resets; one reset per miss keeps the path cheap.
		if resettable, ok := c.Mapper.(interface{ Reset() }); ok {
			resettable.Reset()
			mapping, err = c.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("kube: map %s: %w", gvk.Kind, err)
	}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return c.Dynamic.Resource(mapping.Resource).Namespace(namespace), nil
	}
	return c.Dynamic.Resource(mapping.Resource), nil
}

// StripServerFields removes status and server-populated metadata from an
// applied configuration so skalid never claims ownership of them.
func StripServerFields(obj *unstructured.Unstructured) {
	unstructured.RemoveNestedField(obj.Object, "status")
	removeNullCreationTimestamps(obj.Object)
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(obj.Object, "metadata", "uid")
	unstructured.RemoveNestedField(obj.Object, "metadata", "generation")
}

// The typed zero value of metav1.Time marshals as an explicit null, which
// an apply configuration must not carry; nested template metadata has the
// same problem, so the walk is recursive.
func removeNullCreationTimestamps(value any) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	if timestamp, present := object["creationTimestamp"]; present && timestamp == nil {
		delete(object, "creationTimestamp")
	}
	for _, nested := range object {
		switch typed := nested.(type) {
		case map[string]any:
			removeNullCreationTimestamps(typed)
		case []any:
			for _, element := range typed {
				removeNullCreationTimestamps(element)
			}
		}
	}
}
