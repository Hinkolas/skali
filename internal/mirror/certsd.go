package mirror

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultCertsDir is where dockerd looks up per-registry trust material.
const DefaultCertsDir = "/etc/docker/certs.d"

// InstallDockerCerts writes one registry's docker trust: the cluster CA as
// its verification root plus a CA-signed identity as the docker client
// cert/key (the registry requires cluster mTLS). On workers the node
// identity plays that role, installed at enrollment; the master installs its
// own at serve startup. dockerd reads certs.d per pull — no daemon restart.
//
// baseDir is parameterized for tests; production callers pass
// DefaultCertsDir, which usually needs root — callers treat failure as a
// warning and print what to install manually.
func InstallDockerCerts(baseDir, registryAddr string, caPEM, certPEM, keyPEM []byte) error {
	dir := filepath.Join(baseDir, registryAddr)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mirror: create %s: %w", dir, err)
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"ca.crt", caPEM, 0o644},
		{"client.cert", certPEM, 0o644},
		{"client.key", keyPEM, 0o600},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, f.mode); err != nil {
			return fmt.Errorf("mirror: write %s: %w", filepath.Join(dir, f.name), err)
		}
	}
	return nil
}
