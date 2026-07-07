// Package mirror owns the cluster image mirror: a CNCF distribution registry
// running as a skali system container on the master. The master imports
// upstream images into it digest-pinned; workers pull from it over the LAN
// and need no public egress. Its repository prefix doubles as image
// ownership — automated image GC only ever touches mirror-prefixed images
// (images can't be label-stamped the way containers are).
//
// Trust is the cluster CA end to end: the registry serves a CA-issued cert
// and requires CA-signed client certs, so only enrolled nodes (their node
// identity doubles as the docker client cert, installed under
// /etc/docker/certs.d at enrollment) and the master itself can pull or push.
package mirror

import "time"

// Config locates and provisions the registry. Endpoint host and Port derive
// from CLUSTER_ADDR + REGISTRY_PORT — like join tokens, no cluster address
// means no registry.
type Config struct {
	Endpoint string // host:port every engine in the cluster uses to reach the registry
	DataDir  string // master state dir; material lands under <DataDir>/registry
	Image    string // distribution registry image ref
	Port     int    // host port published on the master
}

// Issuer is the slice of the cluster CA the mirror needs; *cluster.CA
// implements it. An interface (rather than importing cluster) because the
// dependency points the other way too: enrollment installs the registry's
// docker trust via InstallDockerCerts.
type Issuer interface {
	// ServerPEM issues a CA-signed TLS server identity as PEM.
	ServerPEM(cn string, hosts []string, lifetime time.Duration) (certPEM, keyPEM []byte, err error)
	// ClientPEM issues the master's TLS client identity as PEM.
	ClientPEM() (certPEM, keyPEM []byte, err error)
	// CAPEM returns the CA certificate PEM.
	CAPEM() []byte
}
