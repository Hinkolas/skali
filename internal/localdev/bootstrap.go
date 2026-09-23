package localdev

import (
	"context"
	"crypto/tls"
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
	// the full bundle converge. skali dev start --force sets it, so one verb
	// still proves and repairs the whole installation instead of assuming it.
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
	unlock, err := Lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := ObsoletePlatforms(); err != nil {
		return nil, err
	}
	existing, err := LoadState()
	if err != nil && !errors.Is(err, ErrNotInstalled) {
		return nil, err
	}
	if err := CheckVersion(existing); err != nil {
		return nil, err
	}
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
		return nil, errors.New("no skalid image selected: the build or pull above failed; " +
			"fix that or pass --skalid-image")
	}

	statuses, err := Statuses(ctx)
	if err != nil {
		return nil, err
	}
	status := StatusOf(statuses, ClusterName())
	// A cluster without an installation record is not ours to adopt or
	// destroy (an older or foreign installation); the user decides.
	if freshInstall && status != ClusterAbsent {
		return nil, fmt.Errorf("a %s cluster already exists but no local installation record does: "+
			"remove it with `k3d cluster delete %s`, or pick another name via SKALI_DEV_CLUSTER",
			ClusterName(), ClusterName())
	}
	// The development CA lives with the record: generated once for a
	// fresh installation, loaded afterwards, and gone with a reset.
	ca, err := EnsureCA(freshInstall)
	if err != nil {
		return nil, err
	}
	profile := bundleProfile(state, ca)

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
		if err := checkEdgePortsFree(ctx); err != nil {
			return nil, err
		}
		// Create always uses the current pin and the current edge ports; a
		// recreation under retained state must not keep reporting the old
		// cluster's k3s or mappings.
		state.K3sImage = K3sImage
		state.Edge = currentEdgePorts()
		// The record precedes the cluster. A record without a cluster is a
		// plain create on the next pass, while a cluster without a record
		// is refused above as not ours: saving first means no failure
		// between here and the converge-time save (a pull timeout, a failed
		// import) can strand a cluster behind that refusal and a manual
		// k3d cluster delete.
		if err := SaveState(state); err != nil {
			return nil, err
		}
		if err := Create(ctx); err != nil {
			return nil, err
		}
		progress.Done(K3sImage + ", pinned")
	case ClusterStopped:
		progress.Start("Start k3d cluster " + ClusterName())
		if err := checkEdgePortsFree(ctx); err != nil {
			return nil, err
		}
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

	kubeconfig, err := KubeconfigPath()
	if err != nil {
		return nil, err
	}
	client, err := kube.New(kubeconfig)
	if err != nil {
		return nil, err
	}

	// Docker's running bit says nothing about the apiserver; prove it
	// answers (repairing the node-IP crash loop in place, see nodeip.go)
	// before the imports docker-exec into the node and the converge
	// applies against it. A repair reboots the node, so it widens the
	// edge grace below like a fresh start.
	restarted, err := ensureNodeReady(ctx, client, progress)
	if err != nil {
		return nil, err
	}
	justStarted = justStarted || restarted

	// Any node restart silently drops the host gateway entry k3d injected
	// at creation, and a fresh cluster never keeps it (see hostgateway.go);
	// skalid needs it to resolve, so it is repaired on every pass, before
	// the fast path can return, and a repair is proven through the cluster
	// DNS before the pass ends.
	gatewayRepaired, err := ensureHostGateway(ctx, client, progress)
	if err != nil {
		return nil, err
	}
	finish := func() (*State, error) {
		if gatewayRepaired {
			if err := verifyHostGateway(ctx, client, progress); err != nil {
				return nil, err
			}
		}
		return state, nil
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
	// Platform images go first: k3s is pulling its built-in components
	// right now, and every one the import beats is a docker.io round trip
	// saved, while skalid is not deployed until the converge. Each import
	// persists immediately: the fast path below returns before the
	// converge-time save, and a re-import of already-present images is the
	// cost of losing this record.
	if len(missing) > 0 {
		progress.Start("Pull platform images")
		progress.Note(strings.Join(missing, ", "))
		if err := <-pulled; err != nil {
			return nil, err
		}
		progress.Done(fmt.Sprintf("%d cached on the host", len(missing)))
		progress.Start("Import platform images")
		if err := ImportImages(ctx, missing...); err != nil {
			return nil, err
		}
		state.ImportedImages = append(state.ImportedImages, missing...)
		if err := SaveState(state); err != nil {
			return nil, err
		}
		progress.Done(fmt.Sprintf("%d in the cluster", len(missing)))
	}
	progress.Start("Import " + state.SkalidImage)
	if importNeeded {
		if err := ImportImages(ctx, state.SkalidImage); err != nil {
			return nil, err
		}
		state.ImportedImageID = imageID
		if err := SaveState(state); err != nil {
			return nil, err
		}
		progress.Done("")
	} else {
		progress.Skip("unchanged since last import")
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
		bundle.StampedHash(ctx, client) == bundle.Hash(profile) {
		healthy := probeEdge(ctx, ca)
		if !healthy && justStarted {
			progress.Start("Wait for skalid")
			if err := waitEdgeHealthy(ctx, ca); err == nil {
				healthy = true
				progress.Done("answering through the edge")
			} else {
				progress.Skip("not ready, running full converge")
			}
		}
		if healthy {
			progress.Start("Converge platform")
			progress.Skip("unchanged since last converge")
			return finish()
		}
	}

	// The same converge as a production cluster's, stage for stage; every
	// pass is a full converge, so a healthy installation flies through with
	// no-op applies. The admin account rides its own step because
	// production never persists those credentials; locally the record
	// holds them.
	if err := bundle.Converge(ctx, client, profile, progress); err != nil {
		return nil, err
	}
	if err := bundle.EnsureAdminUser(ctx, client, profile, progress); err != nil {
		return nil, err
	}
	if err := SaveState(state); err != nil {
		return nil, err
	}
	// The health proof goes through the TLS edge with the development CA:
	// it proves routing, the platform certificate's issuance, and
	// Traefik's pickup of it end to end before the hash is stamped.
	progress.Start("Wait for skalid")
	if err := waitEdgeHealthy(ctx, ca); err != nil {
		return nil, err
	}
	if err := bundle.StampHash(ctx, client, profile); err != nil {
		return nil, err
	}
	progress.Done(MasterURL())
	return finish()
}

// bundleProfile derives the bundle profile of this installation.
func bundleProfile(state *State, ca *CA) bundle.Profile {
	return bundle.Profile{
		SkalidImage:   state.SkalidImage,
		SkalidImageID: state.ImportedImageID,
		AuthSecret:    state.AuthSecret,
		AdminEmail:    state.AdminEmail,
		AdminPassword: state.AdminPassword,
		RegistryHost:  RegistryHost(),
		Local:         &bundle.Local{CACertPEM: string(ca.CertPEM), CAKeyPEM: string(ca.KeyPEM)},
	}
}

// probeEdge makes one health request to skalid through the local TLS
// edge, dialing the loopback mapping directly and presenting the platform
// host as SNI, verified against the development CA: it proves ingress
// routing, certificate issuance, and Traefik's pickup end to end. (The
// plain-HTTP router only redirects, so it proves nothing here.)
func probeEdge(ctx context.Context, ca *CA) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("https://127.0.0.1:%d/healthz", HTTPSPort()), nil)
	if err != nil {
		return false
	}
	request.Host = bundle.LocalPlatformHost
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{ServerName: bundle.LocalPlatformHost, RootCAs: ca.Pool(), MinVersion: tls.VersionTLS12},
		DisableKeepAlives: true,
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == http.StatusOK
}

// waitEdgeHealthy polls the edge probe until skalid answers.
func waitEdgeHealthy(ctx context.Context, ca *CA) error {
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if probeEdge(ctx, ca) {
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
