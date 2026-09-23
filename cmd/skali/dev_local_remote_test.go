package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/localdev"
)

// localEdge is a fake platform behind a TLS listener whose certificate the
// installation's development CA signed for skali.localhost, exactly what
// the loopback edge presents. The CLI reaches it through the *.localhost
// dial pin, so the master URL stays the real one with the port shifted.
func localEdge(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ca, err := localdev.GenerateCA()
	require.NoError(t, err)
	keyBlock, _ := pem.Decode(ca.KeyPEM)
	caKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	require.NoError(t, err)
	certBlock, _ := pem.Decode(ca.CertPEM)
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	require.NoError(t, err)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: bundle.LocalPlatformHost},
		DNSNames:     []string{bundle.LocalPlatformHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	require.NoError(t, err)

	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	t.Setenv("SKALI_DEV_HTTPS_PORT", port)
	return srv
}

// A local remote recorded before the edge moved to https on the default
// ports still holds the old address; a successful probe with its token
// rewrites the address to the platform's current one, so reauth's exact
// comparison keeps recognizing the local platform.
func TestLoginLocalRemoteFollowsThePlatformAddress(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var seenToken string
	srv := localEdge(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/auth/session", r.URL.Path)
		require.Equal(t, bundle.LocalPlatformHost, r.Host[:len(bundle.LocalPlatformHost)])
		seenToken = r.Header.Get("Authorization")
		w.Header().Set(client.InstanceHeader, "inst-1")
		_ = json.NewEncoder(w).Encode(client.SessionInfo{User: client.User{Email: localdev.AdminEmail}})
	}))
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	seedConfig(t, &cliconfig.Config{Remotes: map[string]*cliconfig.Remote{
		localRemoteName: {Master: "http://skali.localhost:8080", Token: "old-token"},
	}})

	state := &localdev.State{AdminEmail: localdev.AdminEmail, AdminPassword: "pw"}
	require.NoError(t, loginLocalRemote(context.Background(), state))

	require.Equal(t, "Bearer old-token", seenToken, "the recorded session is reused")
	remote := loadConfig(t).Remotes[localRemoteName]
	require.Equal(t, "https://skali.localhost:"+port, remote.Master)
	require.Equal(t, localdev.MasterURL(), remote.Master)
	require.Equal(t, "old-token", remote.Token)
	require.Equal(t, "inst-1", remote.Instance)
	_, err := strconv.Atoi(port)
	require.NoError(t, err)
}
