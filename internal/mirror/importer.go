package mirror

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
)

// Importer sentinels; the REST layer maps them onto status codes.
var (
	ErrInvalidReference    = errors.New("mirror: invalid image reference")
	ErrUpstreamNotFound    = errors.New("mirror: upstream image not found")
	ErrRegistryUnavailable = errors.New("mirror: registry unreachable")
	ErrImageNotFound       = errors.New("mirror: image not in the catalog")
)

// Importer copies upstream images into the cluster registry digest-pinned
// and keeps the catalog table in sync. Copies go registry-to-registry
// through this process — the master's docker daemon is never involved, and
// multi-arch indexes are preserved byte-identical (workers of any
// architecture resolve their platform from the mirrored index).
//
// Only the master has public egress by design: workers never talk upstream.
// Private upstream registries (authenticated pulls) are out of scope for
// now; the upstream side authenticates via the ambient docker keychain,
// which is anonymous on a typical master.
type Importer struct {
	st        *store.Store
	endpoint  string            // the mirror's host:port
	transport http.RoundTripper // mirror-side; upstream uses the default transport
}

// NewImporter builds the importer with its cluster-mTLS transport toward the
// mirror (CA-verified server, master client cert).
func NewImporter(st *store.Store, iss Issuer, endpoint string) (*Importer, error) {
	certPEM, keyPEM, err := iss.ClientPEM()
	if err != nil {
		return nil, err
	}
	clientCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("mirror: client cert: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(iss.CAPEM()) {
		return nil, errors.New("mirror: cannot parse cluster CA PEM")
	}
	return &Importer{st: st, endpoint: endpoint, transport: httpsTransport{&http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			RootCAs:      roots,
			Certificates: []tls.Certificate{clientCert},
		},
	}}}, nil
}

// httpsTransport forces https toward the mirror: go-containerregistry
// defaults RFC1918 registry addresses to plain http, but the mirror serves
// cluster TLS on every address.
type httpsTransport struct{ inner http.RoundTripper }

func (t httpsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "https"
	return t.inner.RoundTrip(req)
}

// List returns the catalog, the authoritative record of what the mirror
// serves (the master is its only writer).
func (i *Importer) List(ctx context.Context) ([]store.RegistryImage, error) {
	return i.st.ListRegistryImages(ctx)
}

// ParseImportReference validates an upstream reference the way Import will:
// it must parse, and it must be a tag (the catalog pins (repository, tag) →
// digest, so a digest reference has no row to land in). Exposed so callers
// that run Import asynchronously can reject bad input synchronously.
func ParseImportReference(reference string) (name.Tag, error) {
	ref, err := name.ParseReference(reference)
	if err != nil {
		return name.Tag{}, fmt.Errorf("%w: %v", ErrInvalidReference, err)
	}
	tag, ok := ref.(name.Tag)
	if !ok {
		return name.Tag{}, fmt.Errorf("%w: digest references are not supported; import a tag", ErrInvalidReference)
	}
	return tag, nil
}

// Import copies one upstream tag into the mirror and records the pin. Tag
// references only (see ParseImportReference). Re-importing moves the pin.
func (i *Importer) Import(ctx context.Context, reference string) (store.RegistryImage, error) {
	tag, err := ParseImportReference(reference)
	if err != nil {
		return store.RegistryImage{}, err
	}

	desc, err := remote.Get(tag, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return store.RegistryImage{}, classifyUpstream(err)
	}

	repo := mirrorRepository(tag)
	dst, err := name.NewTag(fmt.Sprintf("%s/%s:%s", i.endpoint, repo, tag.TagStr()))
	if err != nil {
		return store.RegistryImage{}, fmt.Errorf("mirror: destination reference: %w", err)
	}
	mirrorOpts := []remote.Option{remote.WithContext(ctx), remote.WithTransport(i.transport)}
	// Copies are byte-identical (manifests stream through untouched), so the
	// upstream descriptor digest IS the mirrored digest — the pin.
	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return store.RegistryImage{}, err
		}
		if err := remote.WriteIndex(dst, idx, mirrorOpts...); err != nil {
			return store.RegistryImage{}, classifyMirror(err)
		}
	} else {
		img, err := desc.Image()
		if err != nil {
			return store.RegistryImage{}, err
		}
		if err := remote.Write(dst, img, mirrorOpts...); err != nil {
			return store.RegistryImage{}, classifyMirror(err)
		}
	}

	size, err := blobSize(desc)
	if err != nil {
		// Informational only — never fail a completed copy over it.
		slog.WarnContext(ctx, "compute mirrored image size", "reference", reference, "err", err)
		size = 0
	}

	id, err := uuid.NewV7()
	if err != nil {
		return store.RegistryImage{}, err
	}
	row, err := i.st.UpsertRegistryImage(ctx, store.UpsertRegistryImageParams{
		ID:         id,
		Repository: repo,
		Tag:        tag.TagStr(),
		Digest:     desc.Digest.String(),
		SizeBytes:  size,
	})
	if err != nil {
		return store.RegistryImage{}, err
	}
	slog.InfoContext(ctx, "image imported into the mirror",
		"reference", reference, "repository", repo, "digest", row.Digest, "size", row.SizeBytes)
	return row, nil
}

// Delete removes a catalog entry and its manifest from the registry. Two
// tags pinning the same digest share the manifest — deleting one deletes it
// for both (the survivor 404s on pull until re-imported); acceptable for an
// operator surface. Frees no disk yet: blob GC is a later milestone.
func (i *Importer) Delete(ctx context.Context, id uuid.UUID) error {
	row, err := i.st.GetRegistryImageByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrImageNotFound, id)
	}
	if err != nil {
		return err
	}
	dig, err := name.NewDigest(fmt.Sprintf("%s/%s@%s", i.endpoint, row.Repository, row.Digest))
	if err != nil {
		return fmt.Errorf("mirror: digest reference: %w", err)
	}
	err = remote.Delete(dig, remote.WithContext(ctx), remote.WithTransport(i.transport))
	var terr *transport.Error
	if err != nil && !(errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound) {
		// Already-gone manifests are fine (the row is what's authoritative);
		// anything else keeps the row so the delete can be retried.
		return classifyMirror(err)
	}
	if _, err := i.st.DeleteRegistryImageByID(ctx, id); err != nil {
		return err
	}
	slog.InfoContext(ctx, "image removed from the mirror",
		"repository", row.Repository, "tag", row.Tag, "digest", row.Digest)
	return nil
}

// MirrorRepository resolves an upstream tag reference to its catalog key —
// the mirror-relative repository plus the tag. This is how callers ask "is
// this reference already imported?" against registry_images without
// importing anything.
func MirrorRepository(reference string) (repository, tag string, err error) {
	t, err := ParseImportReference(reference)
	if err != nil {
		return "", "", err
	}
	return mirrorRepository(t), t.TagStr(), nil
}

// mirrorRepository namespaces an upstream repository inside the mirror:
// mirror/<upstream-host>/<path>. The prefix is the ownership boundary later
// GC relies on, and keeping the upstream host avoids collisions between
// registries (docker.io/foo/app vs ghcr.io/foo/app). A port in the upstream
// host becomes a dash — repository path components cannot carry colons.
func mirrorRepository(ref name.Reference) string {
	host := ref.Context().RegistryStr()
	if host == name.DefaultRegistry {
		host = "docker.io"
	}
	host = strings.ReplaceAll(host, ":", "-")
	return "mirror/" + host + "/" + ref.Context().RepositoryStr()
}

// classifyUpstream maps upstream registry failures. 401 doubles as "not
// found": public registries answer anonymous pulls of unknown (and private)
// repositories with unauthorized.
func classifyUpstream(err error) error {
	var terr *transport.Error
	if errors.As(err, &terr) &&
		(terr.StatusCode == http.StatusNotFound || terr.StatusCode == http.StatusUnauthorized || terr.StatusCode == http.StatusForbidden) {
		return fmt.Errorf("%w: %v", ErrUpstreamNotFound, err)
	}
	return fmt.Errorf("mirror: upstream: %w", err)
}

// classifyMirror maps failures talking to our own registry: transport-level
// errors and 5xx mean it is (still) unavailable — the ensure loop may not
// have finished booting it.
func classifyMirror(err error) error {
	var terr *transport.Error
	if errors.As(err, &terr) && terr.StatusCode < http.StatusInternalServerError {
		return fmt.Errorf("mirror: registry: %w", err)
	}
	return fmt.Errorf("%w: %v", ErrRegistryUnavailable, err)
}

// blobSize sums the unique blob bytes behind a manifest or index — config
// and layer blobs deduplicated by digest, child manifests included, so
// multi-arch images with shared layers report what they actually occupy.
func blobSize(desc *remote.Descriptor) (int64, error) {
	seen := map[v1.Hash]bool{}
	var total int64
	add := func(h v1.Hash, n int64) {
		if !seen[h] {
			seen[h] = true
			total += n
		}
	}
	var walkIndex func(idx v1.ImageIndex) error
	addImage := func(img v1.Image, manifestSize int64) error {
		m, err := img.Manifest()
		if err != nil {
			return err
		}
		add(m.Config.Digest, m.Config.Size)
		for _, l := range m.Layers {
			add(l.Digest, l.Size)
		}
		total += manifestSize
		return nil
	}
	walkIndex = func(idx v1.ImageIndex) error {
		im, err := idx.IndexManifest()
		if err != nil {
			return err
		}
		for _, m := range im.Manifests {
			switch {
			case m.MediaType.IsIndex():
				child, err := idx.ImageIndex(m.Digest)
				if err != nil {
					return err
				}
				if err := walkIndex(child); err != nil {
					return err
				}
			case m.MediaType.IsImage():
				img, err := idx.Image(m.Digest)
				if err != nil {
					return err
				}
				if err := addImage(img, m.Size); err != nil {
					return err
				}
			}
		}
		return nil
	}

	switch {
	case desc.MediaType.IsIndex():
		idx, err := desc.ImageIndex()
		if err != nil {
			return 0, err
		}
		if err := walkIndex(idx); err != nil {
			return 0, err
		}
		total += int64(len(desc.Manifest))
	case desc.MediaType.IsImage():
		img, err := desc.Image()
		if err != nil {
			return 0, err
		}
		if err := addImage(img, int64(len(desc.Manifest))); err != nil {
			return 0, err
		}
	}
	return total, nil
}
