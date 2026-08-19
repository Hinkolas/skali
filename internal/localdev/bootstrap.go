package localdev

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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

// Progress receives the ensure stages as they happen; the shape is shared
// with the bundle converge. A nil Progress is silent.
type Progress = bundle.Progress

type silentProgress struct{}

func (silentProgress) Start(string) {}
func (silentProgress) Done(string)  {}
func (silentProgress) Skip(string)  {}
func (silentProgress) Note(string)  {}

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
	switch {
	case errors.Is(err, errK3dMissing):
		progress.Done(fmt.Sprintf("docker %s, k3d missing", docker))
		progress.Start("Install k3d v" + k3dPinnedVersion)
		binary, installErr := installK3d(ctx)
		if installErr != nil {
			return nil, fmt.Errorf("install k3d v%s: %w (or install k3d >= %s yourself: %s)",
				k3dPinnedVersion, installErr, k3dMinVersion, k3dInstallHint())
		}
		progress.Done(binary)
	case err != nil:
		return nil, err
	default:
		progress.Done(fmt.Sprintf("docker %s, k3d %s", docker, k3dVersion))
	}

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
		return nil, errors.New("no skalid image selected: pass --skalid-image " +
			"(working from the skali repository, task dev:image builds one)")
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
	// A cluster from before the single-container layout cannot be reshaped
	// in place (its load balancer resolves the node by the old name); the
	// platform is disposable by design, so recreation is the migration.
	if status != ClusterAbsent && legacyLayout(ctx) {
		return nil, fmt.Errorf("the %s cluster predates the single-container layout: "+
			"recreate it with `skali dev reset`, then run `skali dev` again", ClusterName())
	}
	// Loopback service ports (postgres, S3) are create-time k3d options: a
	// cluster from before them cannot be reshaped in place either.
	if status != ClusterAbsent {
		hasPorts, err := HasLoopbackPortMaps(ctx)
		if err != nil {
			return nil, err
		}
		if !hasPorts {
			return nil, fmt.Errorf("the %s cluster predates the loopback service port maps: "+
				"recreate it with `skali dev reset`, then run `skali dev` again", ClusterName())
		}
	}

	// Public platform images pre-pull on the host in parallel with the
	// cluster work below and land in one batched import, so a cold cluster
	// never pulls from the internet mid-deploy. The record only counts on a
	// cluster that exists; fresh containerd starts empty.
	if status == ClusterAbsent {
		state.ImportedImages = nil
	}
	imported := make(map[string]bool, len(state.ImportedImages))
	for _, image := range state.ImportedImages {
		imported[image] = true
	}
	var missing []string
	for _, image := range RequiredImages() {
		if !imported[image] {
			missing = append(missing, image)
		}
	}
	var pulled chan error
	if len(missing) > 0 {
		pulled = make(chan error, 1)
		go func() { pulled <- ensureHostImages(ctx, missing) }()
	}

	justStarted := false
	switch status {
	case ClusterAbsent:
		progress.Start("Create k3d cluster " + ClusterName())
		if err := Create(ctx); err != nil {
			return nil, err
		}
		// Create always uses the current pin; a recreation under retained
		// state must not keep reporting the old cluster's k3s.
		state.K3sImage = K3sImage
		progress.Done(K3sImage + ", pinned")
	case ClusterStopped:
		progress.Start("Start k3d cluster " + ClusterName())
		if err := Start(ctx); err != nil {
			return nil, err
		}
		status, justStarted = ClusterRunning, true
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
	var batch []string
	if importNeeded {
		batch = append(batch, state.SkalidImage)
	}
	if len(missing) > 0 {
		progress.Start("Pull platform images")
		progress.Note(strings.Join(missing, ", "))
		if err := <-pulled; err != nil {
			return nil, err
		}
		progress.Done(fmt.Sprintf("%d cached on the host", len(missing)))
		batch = append(batch, missing...)
	}
	progress.Start("Import " + state.SkalidImage)
	if len(batch) > 0 {
		if err := ImportImages(ctx, batch...); err != nil {
			return nil, err
		}
		state.ImportedImageID = imageID
		state.ImportedImages = append(state.ImportedImages, missing...)
		// Persist immediately: the fast path below returns before the
		// converge-time save, and a re-import of already-present images is
		// the cost of losing this record.
		if err := SaveState(state); err != nil {
			return nil, err
		}
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

	// The fast path: on a running cluster with an unchanged image, a bundle
	// hash matching the stamp of the last completed converge plus one live
	// health probe through the edge prove the platform current for the
	// price of two round trips instead of a full no-op apply pass. A
	// cluster that just restarted gets the full probe window instead of a
	// single attempt: skalid is still coming back, and one failed probe
	// would silently buy the whole converge. Anything off (a rebuilt CLI,
	// a changed profile, a missing stamp, an unhealthy skalid) falls
	// through to the converge.
	if !opts.ForceConverge && status == ClusterRunning && !importNeeded &&
		bundle.StampedHash(ctx, client) == bundle.Hash(bundleProfile(state)) {
		healthy := probeEdge(ctx)
		if !healthy && justStarted {
			progress.Start("Wait for skalid")
			if err := waitEdgeHealthy(ctx); err == nil {
				healthy = true
				progress.Done("answering through the edge")
			} else {
				progress.Skip("not ready, running full converge")
			}
		}
		if healthy {
			progress.Start("Converge platform")
			progress.Skip("unchanged since last converge")
			return state, nil
		}
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
	if err := bundle.StampHash(ctx, client, bundleProfile(state)); err != nil {
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
	if err := applier.ApplyObjects(ctx, objects.Priority); err != nil {
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
	if err := applier.ApplyObjectsRetry(ctx, objects.Database, 2*time.Minute); err != nil {
		return err
	}
	if err := applier.WaitClusterReady(ctx, bundle.Namespace, "skali-db", 1); err != nil {
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
