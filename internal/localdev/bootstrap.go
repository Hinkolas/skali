package localdev

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/kube"
)

// EnsureOptions parameterize one Ensure pass.
type EnsureOptions struct {
	// SkalidImage overrides the control-plane image; empty keeps the
	// recorded one.
	SkalidImage string
	// Log narrates progress lines ("  ok  Create k3d cluster skali-dev").
	Log func(format string, args ...any)
}

// Ensure brings the local platform up, idempotently: prerequisites, the
// k3d cluster (created or restarted with state retained), the skali-system
// bundle stages in order, and the bootstrap operator user. It returns the
// installation state for login.
func Ensure(ctx context.Context, opts EnsureOptions) (*State, error) {
	log := opts.Log
	if log == nil {
		log = func(string, ...any) {}
	}

	docker, k3dVersion, err := CheckPrerequisites(ctx)
	if err != nil {
		return nil, err
	}
	log("  ok  Check prerequisites: docker %s, k3d %s", docker, k3dVersion)

	freshInstall := false
	state, err := LoadState()
	if errors.Is(err, ErrNotInstalled) {
		freshInstall = true
		state, err = NewState(opts.SkalidImage)
	}
	if err != nil {
		return nil, err
	}
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
		if err := Create(ctx); err != nil {
			return nil, err
		}
		log("  ok  Create k3d cluster %s (%s, pinned)", ClusterName(), K3sImage)
	case ClusterStopped:
		if err := Start(ctx); err != nil {
			return nil, err
		}
		log("  ok  Start k3d cluster %s (state retained)", ClusterName())
	case ClusterRunning:
		if err := WriteKubeconfig(ctx); err != nil {
			return nil, err
		}
	}

	if err := ImportImage(ctx, state.SkalidImage); err != nil {
		return nil, err
	}
	log("  ok  Import %s", state.SkalidImage)

	kubeconfig, err := KubeconfigPath()
	if err != nil {
		return nil, err
	}
	client, err := kube.New(kubeconfig)
	if err != nil {
		return nil, err
	}
	if err := applyBundle(ctx, client, state, log); err != nil {
		return nil, err
	}
	if err := SaveState(state); err != nil {
		return nil, err
	}
	if err := waitEdgeHealthy(ctx); err != nil {
		return nil, err
	}
	log("  ok  skalid (%s)", MasterURL())
	return state, nil
}

// applyBundle drives the ordered stages; every pass is a full converge, so
// a healthy installation flies through with no-op applies.
func applyBundle(ctx context.Context, client *kube.Client, state *State, log func(string, ...any)) error {
	applier := &bundle.Applier{Client: client}
	objects, err := bundle.Render(bundle.Profile{
		SkalidImage:   state.SkalidImage,
		AuthSecret:    state.AuthSecret,
		AdminEmail:    state.AdminEmail,
		AdminPassword: state.AdminPassword,
		RegistryHost:  RegistryHost(),
	})
	if err != nil {
		return err
	}

	if err := applier.ApplyObjects(ctx, objects.Namespace); err != nil {
		return err
	}
	if err := applier.ApplyManifest(ctx, bundle.CNPGManifest()); err != nil {
		return err
	}
	if err := applier.WaitDeploymentReady(ctx, "cnpg-system", "cnpg-controller-manager"); err != nil {
		return err
	}
	log("        ok  Blessed operators: CNPG %s, Traefik (k3s)", bundle.CNPGVersion)

	// Webhook-validated objects race their operator's serving certs; the
	// retry absorbs the warm-up window.
	if err := applyWithRetry(ctx, applier, objects.Database); err != nil {
		return err
	}
	if err := applier.WaitClusterReady(ctx, bundle.Namespace, "skali-db"); err != nil {
		return err
	}
	log("        ok  Bootstrap database (tier: single)")

	if err := applier.ApplyObjects(ctx, objects.Registry); err != nil {
		return err
	}
	if err := applier.WaitDeploymentReady(ctx, bundle.Namespace, "skali-registry"); err != nil {
		return err
	}
	log("        ok  Managed registry (%s for pushes)", RegistryHost())

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

// waitEdgeHealthy polls skalid's health through the local edge, proving
// ingress routing end to end.
func waitEdgeHealthy(ctx context.Context) error {
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(3 * time.Minute)
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", HTTPPort())
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		request.Host = "skali.localhost"
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
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

// Reset destroys the complete local installation: cluster, volumes, and
// the state record.
func Reset(ctx context.Context) error {
	status, err := Status(ctx)
	if err == nil && status != ClusterAbsent {
		if err := Delete(ctx); err != nil {
			return err
		}
	}
	return RemoveState()
}
