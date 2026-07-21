package localdev

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/kube"
)

// EnsureOptions parameterize one Ensure pass.
type EnsureOptions struct {
	// SkalidImage overrides the control-plane image; empty keeps the
	// recorded one.
	SkalidImage string
	// ForceConverge skips the unchanged-platform fast path and always runs
	// the full bundle converge. skali dev up sets it, so one verb still
	// proves and repairs the whole installation instead of assuming it.
	ForceConverge bool
	// Progress narrates the ensure stages.
	Progress Progress
}

// Progress receives the ensure stages as they happen: Start begins a
// stage, Done concludes the running one with optional detail, Skip
// concludes it as not needed with the reason. A stage that errors is
// never concluded; Ensure's caller settles it from the returned error.
// A nil Progress is silent.
type Progress interface {
	Start(title string)
	Done(detail string)
	Skip(detail string)
}

type silentProgress struct{}

func (silentProgress) Start(string) {}
func (silentProgress) Done(string)  {}
func (silentProgress) Skip(string)  {}

// Ensure brings the local platform up, idempotently: prerequisites, the
// k3d cluster (created or restarted with state retained), the skali-system
// bundle stages in order, and the bootstrap operator user. It returns the
// installation state for login.
func Ensure(ctx context.Context, opts EnsureOptions) (*State, error) {
	progress := opts.Progress
	if progress == nil {
		progress = silentProgress{}
	}

	progress.Start("Check prerequisites")
	docker, k3dVersion, err := CheckPrerequisites(ctx)
	if err != nil {
		return nil, err
	}
	progress.Done(fmt.Sprintf("docker %s, k3d %s", docker, k3dVersion))

	freshInstall := false
	state, err := LoadState()
	if errors.Is(err, ErrNotInstalled) {
		freshInstall = true
		state, err = NewState(opts.SkalidImage)
	}
	if err != nil {
		return nil, err
	}
	// The recorded tag pairs with ImportedImageID: containerd holds the
	// imported bits under exactly this name.
	importedTag := state.SkalidImage
	if opts.SkalidImage != "" {
		state.SkalidImage = opts.SkalidImage
	}
	if state.SkalidImage == "" {
		return nil, errors.New("no skalid image selected: run task dev:image or pass --skalid-image")
	}

	status, err := Status(ctx)
	if err != nil {
		return nil, err
	}
	// A cluster without an installation record is not ours to adopt or
	// destroy (an older or foreign installation); the user decides.
	if freshInstall && status != ClusterAbsent {
		return nil, fmt.Errorf("a %s cluster already exists but no local installation record does: "+
			"remove it with `k3d cluster delete %s`, or pick another name via SKALI_DEV_CLUSTER",
			ClusterName(), ClusterName())
	}
	switch status {
	case ClusterAbsent:
		progress.Start("Create k3d cluster " + ClusterName())
		if err := Create(ctx); err != nil {
			return nil, err
		}
		progress.Done(K3sImage + ", pinned")
	case ClusterStopped:
		progress.Start("Start k3d cluster " + ClusterName())
		if err := Start(ctx); err != nil {
			return nil, err
		}
		progress.Done("state retained")
	case ClusterRunning:
		if err := WriteKubeconfig(ctx); err != nil {
			return nil, err
		}
	}

	// The docker image ID is the content identity behind the mutable dev
	// tag; a matching record means the cluster already holds these exact
	// bits under this exact name (containerd resolves by tag, so a mere
	// re-tag of identical content still needs the import). A just-created
	// cluster starts with empty containerd, so the record cannot be
	// trusted there.
	imageID, err := ImageID(ctx, state.SkalidImage)
	if err != nil {
		return nil, err
	}
	importNeeded := status == ClusterAbsent || state.SkalidImage != importedTag || imageID != state.ImportedImageID
	progress.Start("Import " + state.SkalidImage)
	if importNeeded {
		if err := ImportImage(ctx, state.SkalidImage); err != nil {
			return nil, err
		}
		state.ImportedImageID = imageID
		progress.Done("")
	} else {
		progress.Skip("unchanged since last import")
	}

	kubeconfig, err := KubeconfigPath()
	if err != nil {
		return nil, err
	}
	client, err := kube.New(kubeconfig)
	if err != nil {
		return nil, err
	}

	// The fast path: on an already-running cluster with an unchanged image,
	// a bundle hash matching the stamp of the last completed converge plus
	// one live health probe through the edge prove the platform current for
	// the price of two round trips instead of a full no-op apply pass.
	// Anything off (a rebuilt CLI, a changed profile, a missing stamp, an
	// unhealthy skalid) falls through to the converge.
	if !opts.ForceConverge && status == ClusterRunning && !importNeeded &&
		stampedBundleHash(ctx, client) == bundle.Hash(bundleProfile(state)) && probeEdge(ctx) {
		progress.Start("Converge platform")
		progress.Skip("unchanged since last converge")
		return state, nil
	}

	if err := applyBundle(ctx, client, state, progress); err != nil {
		return nil, err
	}
	if err := SaveState(state); err != nil {
		return nil, err
	}
	if err := waitEdgeHealthy(ctx); err != nil {
		return nil, err
	}
	if err := stampBundleHash(ctx, client, bundleProfile(state)); err != nil {
		return nil, err
	}
	progress.Done(MasterURL())
	return state, nil
}

// bundleProfile derives the bundle profile of this installation.
func bundleProfile(state *State) bundle.Profile {
	return bundle.Profile{
		SkalidImage:   state.SkalidImage,
		SkalidImageID: state.ImportedImageID,
		AuthSecret:    state.AuthSecret,
		AdminEmail:    state.AdminEmail,
		AdminPassword: state.AdminPassword,
		RegistryHost:  RegistryHost(),
	}
}

// stampedBundleHash reads the hash stamped by the last completed converge;
// absent or unreadable reads as empty, which matches no bundle.
func stampedBundleHash(ctx context.Context, client *kube.Client) string {
	namespace, err := client.Clientset.CoreV1().Namespaces().Get(ctx, bundle.Namespace, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return namespace.Annotations[bundle.HashAnnotation]
}

// stampBundleHash re-applies the namespace stage with the bundle hash
// annotation added, under the same installer field manager, so the plain
// namespace apply at the start of the next converge clears the stamp again.
func stampBundleHash(ctx context.Context, client *kube.Client, profile bundle.Profile) error {
	objects, err := bundle.Render(profile)
	if err != nil {
		return err
	}
	hash := bundle.Hash(profile)
	stamped := make([]unstructured.Unstructured, 0, len(objects.Namespace))
	for _, object := range objects.Namespace {
		annotated := object.DeepCopy()
		annotations := annotated.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[bundle.HashAnnotation] = hash
		annotated.SetAnnotations(annotations)
		stamped = append(stamped, *annotated)
	}
	applier := &bundle.Applier{Client: client}
	return applier.ApplyObjects(ctx, stamped)
}

// applyBundle drives the ordered stages; every pass is a full converge, so
// a healthy installation flies through with no-op applies. The final
// skalid stage stays open for Ensure's edge health check.
func applyBundle(ctx context.Context, client *kube.Client, state *State, progress Progress) error {
	applier := &bundle.Applier{Client: client}
	objects, err := bundle.Render(bundleProfile(state))
	if err != nil {
		return err
	}

	progress.Start("Install blessed operators")
	if err := applier.ApplyObjects(ctx, objects.Namespace); err != nil {
		return err
	}
	if err := applier.ApplyManifest(ctx, bundle.CNPGManifest()); err != nil {
		return err
	}
	if err := applier.WaitDeploymentReady(ctx, "cnpg-system", "cnpg-controller-manager"); err != nil {
		return err
	}
	progress.Done("CNPG " + bundle.CNPGVersion + ", Traefik (k3s)")

	// Webhook-validated objects race their operator's serving certs; the
	// retry absorbs the warm-up window.
	progress.Start("Bootstrap database")
	if err := applyWithRetry(ctx, applier, objects.Database); err != nil {
		return err
	}
	if err := applier.WaitClusterReady(ctx, bundle.Namespace, "skali-db"); err != nil {
		return err
	}
	progress.Done("tier: single")

	progress.Start("Start managed registry")
	if err := applier.ApplyObjects(ctx, objects.Registry); err != nil {
		return err
	}
	if err := applier.WaitDeploymentReady(ctx, bundle.Namespace, "skali-registry"); err != nil {
		return err
	}
	progress.Done(RegistryHost() + " for pushes")

	progress.Start("Start skalid")
	if err := applier.ApplyObjects(ctx, objects.Skalid); err != nil {
		return err
	}
	if err := applier.WaitDeploymentReady(ctx, bundle.Namespace, "skalid"); err != nil {
		return err
	}
	if err := applier.ApplyObjects(ctx, objects.BootstrapUser); err != nil {
		return err
	}
	if err := applier.WaitJobComplete(ctx, bundle.Namespace, "skali-bootstrap-user"); err != nil {
		return err
	}
	return nil
}

func applyWithRetry(ctx context.Context, applier *bundle.Applier, objects []unstructured.Unstructured) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err := applier.ApplyObjects(ctx, objects)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// probeEdge makes one health request to skalid through the local edge,
// proving ingress routing end to end.
func probeEdge(ctx context.Context) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/healthz", HTTPPort()), nil)
	if err != nil {
		return false
	}
	request.Host = "skali.localhost"
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == http.StatusOK
}

// waitEdgeHealthy polls the edge probe until skalid answers.
func waitEdgeHealthy(ctx context.Context) error {
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if probeEdge(ctx) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("localdev: skalid never became healthy through the edge at " + MasterURL())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
