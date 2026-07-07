package cluster

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/Hinkolas/skali/internal/store"
)

// ConnPool caches one gRPC client connection per node, dialed with the
// master's cluster identity. Per-call dialing was fine while the heartbeat
// was the only RPC; container lifecycle calls share these connections
// instead. grpc.NewClient is lazy and self-healing, so a cached conn is
// safe to keep across worker restarts.
//
// Authentication per connection: the worker's cert must chain to the cluster
// CA, carry the node's UUID as SAN (standard hostname verification via
// ServerName), and match the cert serial recorded at enrollment. Get
// re-checks addr and serial against the fresh node row on every call, so a
// re-enrolled or re-addressed node is re-dialed and its old cert refused.
type ConnPool struct {
	ca         *CA
	clientCert tls.Certificate

	mu    sync.Mutex
	conns map[uuid.UUID]*nodeConn
}

type nodeConn struct {
	conn   *grpc.ClientConn
	addr   string
	serial string
}

// NewConnPool mints the master's dialing identity from the CA (in memory;
// the master holds the CA key anyway).
func NewConnPool(ca *CA) (*ConnPool, error) {
	clientCert, err := ca.IssueClientCert()
	if err != nil {
		return nil, err
	}
	return &ConnPool{ca: ca, clientCert: clientCert, conns: map[uuid.UUID]*nodeConn{}}, nil
}

// Get returns the node's connection, dialing (or re-dialing) when none
// exists or the node's addr/cert serial changed since the cached dial.
func (p *ConnPool) Get(node store.Node) (*grpc.ClientConn, error) {
	serial := ""
	if node.CertSerial != nil {
		serial = *node.CertSerial
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if nc, ok := p.conns[node.ID]; ok {
		if nc.addr == node.AdvertiseAddr && nc.serial == serial {
			return nc.conn, nil
		}
		_ = nc.conn.Close()
		delete(p.conns, node.ID)
	}

	conn, err := grpc.NewClient(node.AdvertiseAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(p.dialTLS(node.ID, serial))))
	if err != nil {
		return nil, err
	}
	p.conns[node.ID] = &nodeConn{conn: conn, addr: node.AdvertiseAddr, serial: serial}
	return conn, nil
}

// Retain drops cached connections for nodes no longer in the cluster; the
// poller calls it with each tick's node list so deleted nodes don't leak
// connections.
func (p *ConnPool) Retain(ids map[uuid.UUID]struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, nc := range p.conns {
		if _, ok := ids[id]; !ok {
			_ = nc.conn.Close()
			delete(p.conns, id)
		}
	}
}

func (p *ConnPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, nc := range p.conns {
		_ = nc.conn.Close()
		delete(p.conns, id)
	}
}

// dialTLS authenticates one specific worker. The serial is pinned at dial
// time; Get re-dials whenever the recorded serial changes, so a deleted or
// re-enrolled node's old cert is refused even though the CA once signed it.
func (p *ConnPool) dialTLS(nodeID uuid.UUID, serial string) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{p.clientCert},
		RootCAs:      p.ca.Pool(),
		ServerName:   nodeID.String(),
		VerifyPeerCertificate: func(_ [][]byte, chains [][]*x509.Certificate) error {
			if len(chains) == 0 || len(chains[0]) == 0 {
				return fmt.Errorf("cluster: no verified chain")
			}
			if got := chains[0][0].SerialNumber.Text(16); serial == "" || got != serial {
				return fmt.Errorf("cluster: node %s presented unexpected cert serial", nodeID)
			}
			return nil
		},
	}
}
