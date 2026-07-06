package cluster

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// Worker identity layout under DATA_DIR, written once by `skalid enroll` and
// read by `skalid agent`:
//
//	ca.crt     0644  cluster CA cert (PEM) — the worker's trust root
//	node.crt   0644  node cert signed by the CA (PEM)
//	node.key   0600  node private key (PEM)
//	node.json  0600  node id + addresses
const (
	caCertFile   = "ca.crt"
	nodeCertFile = "node.crt"
	nodeKeyFile  = "node.key"
	nodeMetaFile = "node.json"
)

// Identity is a worker's enrolled cluster identity.
type Identity struct {
	NodeID        uuid.UUID
	MasterAddr    string
	AdvertiseAddr string
	Cert          tls.Certificate
	CACert        *x509.Certificate
	CAPool        *x509.CertPool
}

type nodeMeta struct {
	NodeID        uuid.UUID `json:"node_id"`
	MasterAddr    string    `json:"master_addr"`
	AdvertiseAddr string    `json:"advertise_addr"`
}

// SaveIdentity persists an enrollment result. It overwrites any previous
// identity — re-enrolling a machine replaces it.
func SaveIdentity(dir string, meta Identity, keyPEM, certPEM, caPEM []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cluster: create data dir: %w", err)
	}
	metaJSON, err := json.MarshalIndent(nodeMeta{
		NodeID:        meta.NodeID,
		MasterAddr:    meta.MasterAddr,
		AdvertiseAddr: meta.AdvertiseAddr,
	}, "", "  ")
	if err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{caCertFile, caPEM, 0o644},
		{nodeCertFile, certPEM, 0o644},
		{nodeKeyFile, keyPEM, 0o600},
		{nodeMetaFile, metaJSON, 0o600},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, f.mode); err != nil {
			return fmt.Errorf("cluster: write %s: %w", f.name, err)
		}
	}
	return nil
}

// LoadIdentity reads the identity `skalid enroll` wrote.
func LoadIdentity(dir string) (*Identity, error) {
	metaJSON, err := os.ReadFile(filepath.Join(dir, nodeMetaFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("cluster: no node identity in %s — run `skalid enroll` first", dir)
		}
		return nil, err
	}
	var meta nodeMeta
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return nil, fmt.Errorf("cluster: parse %s: %w", nodeMetaFile, err)
	}

	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, nodeCertFile), filepath.Join(dir, nodeKeyFile))
	if err != nil {
		return nil, fmt.Errorf("cluster: load node cert/key: %w", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, caCertFile))
	if err != nil {
		return nil, fmt.Errorf("cluster: load CA cert: %w", err)
	}
	caCert, err := parseCertPEM(caPEM)
	if err != nil {
		return nil, fmt.Errorf("cluster: parse CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return &Identity{
		NodeID:        meta.NodeID,
		MasterAddr:    meta.MasterAddr,
		AdvertiseAddr: meta.AdvertiseAddr,
		Cert:          cert,
		CACert:        caCert,
		CAPool:        pool,
	}, nil
}
