// Package registry is skalid's client of the managed OCI registry. Its
// core job is digest verification: an artifact only verifies after the
// registry itself confirms the manifest is present, because client-reported
// success is never trusted as deployment state. Repository layout helpers
// pin the contract naming (release artifacts under skali/<project>/<app>,
// imported upstream content under cache/<host>/<path>). Against a
// token-authenticated registry the client self-issues pull tokens through
// TokenSource; retention and garbage collection are still to come.
package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

var (
	// ErrDisabled: no managed registry is configured; build and import
	// surfaces are unavailable.
	ErrDisabled = errors.New("registry: no managed registry configured")
	// ErrManifestNotFound: the registry does not hold the claimed digest.
	ErrManifestNotFound = errors.New("registry: manifest not found")
	// ErrUnavailable: the registry did not answer.
	ErrUnavailable = errors.New("registry: unavailable")
)

// Client verifies content in the managed registry.
type Client struct {
	// Host names the registry in artifact references (what nodes pull and
	// build clients push, for example localhost:5510). Empty disables the
	// registry surfaces.
	Host string
	// Endpoint is the address skalid itself dials for verification (the
	// in-cluster service in bundle installations); empty falls back to
	// Host.
	Endpoint string
	// Insecure permits plain HTTP; the anonymous loopback-only local
	// registry uses it.
	Insecure bool
	// TokenSource mints a registry token for one repository when the
	// registry requires token auth. skalid holds the signing key, so it
	// self-issues instead of round-tripping through the public realm. Nil
	// keeps requests anonymous (the local registry).
	TokenSource func(repository string, actions []string) (string, error)
}

func (c *Client) Disabled() bool { return c == nil || c.Host == "" }

func (c *Client) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return c.Host
}

// VerifyManifest confirms the repository holds the digest, by HEAD against
// the manifest endpoint.
func (c *Client) VerifyManifest(ctx context.Context, repository, digest string) error {
	if c.Disabled() {
		return ErrDisabled
	}
	var opts []name.Option
	if c.Insecure {
		opts = append(opts, name.Insecure)
	}
	ref, err := name.NewDigest(fmt.Sprintf("%s/%s@%s", c.endpoint(), repository, digest), opts...)
	if err != nil {
		return fmt.Errorf("registry: parse %s@%s: %w", repository, digest, err)
	}
	remoteOpts := []remote.Option{remote.WithContext(ctx)}
	if c.TokenSource != nil {
		token, err := c.TokenSource(repository, []string{"pull"})
		if err != nil {
			return fmt.Errorf("registry: mint pull token for %s: %w", repository, err)
		}
		remoteOpts = append(remoteOpts, remote.WithAuth(authn.FromConfig(authn.AuthConfig{RegistryToken: token})))
	}
	descriptor, err := remote.Head(ref, remoteOpts...)
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %s@%s", ErrManifestNotFound, repository, digest)
		}
		return fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	if descriptor.Digest.String() != digest {
		return fmt.Errorf("%w: registry answered %s for %s", ErrManifestNotFound, descriptor.Digest, digest)
	}
	return nil
}

// ReleaseRepo is the repository for built release artifacts.
func ReleaseRepo(project, application string) string {
	return "skali/" + project + "/" + application
}

// CacheRepo is the deterministic cache repository for one upstream
// reference: cache/<registry host>/<repository path>, dropping tag and
// digest. Docker Hub short names normalize through their canonical
// registry and library prefix.
func CacheRepo(upstream string) (string, error) {
	ref, err := name.ParseReference(upstream)
	if err != nil {
		return "", fmt.Errorf("registry: parse upstream %s: %w", upstream, err)
	}
	repo := ref.Context()
	return "cache/" + repo.RegistryStr() + "/" + repo.RepositoryStr(), nil
}

// PushRef assembles the full pushable reference for a repository on the
// managed registry. The tag only names what build clients push; identity
// is always the digest.
func (c *Client) PushRef(repository, tag string) string {
	if tag == "" {
		tag = "latest"
	}
	return c.Host + "/" + strings.TrimPrefix(repository, "/") + ":" + tag
}
