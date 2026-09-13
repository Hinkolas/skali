package main

import (
	"fmt"
	"sync"

	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// skewRecorder holds the daemon version a remote-backed client observed in
// this invocation; main prints one hint from it after the command, and a
// dispatched child reads it to tell its parent the daemon moved. Clients
// built by remoteClient feed it on every response; remote add and remote
// login feed it from their probe once the hand-off to the cluster's
// release declined (the login is then refused for the wrong release, and
// the hint names the fix). The local dev login stays quiet.
type skewRecorder struct {
	mu     sync.Mutex
	remote string // remote name, "" when the master matches no stored remote
	server string
}

var skew skewRecorder

func (s *skewRecorder) record(remote, server string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remote, s.server = remote, server
}

func (s *skewRecorder) snapshot() (remote, server string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remote, s.server
}

func (s *skewRecorder) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remote, s.server = "", ""
}

// pendingSkewHint is the line main prints after a command: the skew between
// this CLI and the daemon it talked to, if any.
func pendingSkewHint() string {
	remote, server := skew.snapshot()
	if server == "" {
		return ""
	}
	return skewHint(remote, versionpkg.Version, server)
}

// skewHint names a release skew between this CLI and a remote's daemon in
// one line ending in the fix; empty when either side is not a release or
// they match. Dispatch (docs/versioning.md, decision 1) normally closes the
// gap before a command runs; the hint remains for development builds,
// SKALI_NO_DISPATCH, and a fetch that failed, where skali upgrade --version
// moves the CLI in either direction. The local platform is skali dev's:
// behind the CLI it moves with skali dev upgrade, ahead of it the CLI
// follows or the platform is recreated.
func skewHint(remote, cli, server string) string {
	if !versionpkg.ReleasesDiffer(cli, server) {
		return ""
	}
	if remote == localRemoteName {
		if versionpkg.Older(server, cli) {
			return fmt.Sprintf("hint: the local platform runs skalid %s and this CLI is %s; run skali dev upgrade to move it",
				server, cli)
		}
		return fmt.Sprintf("hint: the local platform runs skalid %s and this CLI is %s; run skali upgrade --version %s to match it, "+
			"or skali dev reset to recreate it at this CLI's version", server, cli, server)
	}
	subject := "the remote"
	if remote != "" {
		subject = "remote " + remote
	}
	return fmt.Sprintf("hint: %s runs skalid %s and this CLI is %s; run skali upgrade --version %s to match it, or download it from %s",
		subject, server, cli, server, versionpkg.ReleasePageURL(releaseBase(), server))
}

// devSkewError refuses to drive a released local platform from a released
// CLI of another version, before any request the daemon would refuse with
// cli_version_mismatch. Development builds and working-tree or custom
// images have no comparable version and pass.
func devSkewError(image string) error {
	platform, ok := versionpkg.PublishedSkalidVersion(image)
	if !ok {
		return nil
	}
	if hint := skewHint(localRemoteName, versionpkg.Version, platform); hint != "" {
		return fmt.Errorf("%s", hint[len("hint: "):])
	}
	return nil
}
