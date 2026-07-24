package clusterstate

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Hinkolas/skali/internal/layout"
)

func TestAgentPollDiscoversEveryActiveCoordinator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	nodeID := "4f0e442c-717b-4721-9468-4d729dc16a4e"
	state, err := NewSeedState("e2e", Node{
		ID: nodeID, InstallationID: "seed-install", Name: "seed-1",
		Role: layout.RoleServer, Capabilities: append([]string(nil), layout.Capabilities...),
		Coordinator: "https://10.1.0.3:6444",
	}, now)
	require.NoError(t, err)
	store := &Store{Client: fake.NewSimpleClientset(), Now: func() time.Time { return now }}
	_, err = store.Bootstrap(ctx, state)
	require.NoError(t, err)

	body, err := json.Marshal(AgentPollRequest{})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/v1/agent/poll", bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{
		Subject: pkix.Name{
			CommonName: nodeID, Organization: []string{"skali-nodes"},
		},
	}}}}
	response := httptest.NewRecorder()

	(&Coordinator{Store: store}).Handler().ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var poll AgentPollResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &poll))
	require.Equal(t, []string{"https://10.1.0.3:6444"}, poll.Coordinators)
}

func TestPinnedEnrollmentAndIdempotentCSRRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	seedID := "4f0e442c-717b-4721-9468-4d729dc16a4e"
	state, err := NewSeedState("e2e", Node{
		ID: seedID, InstallationID: "seed-install", Name: "seed-1",
		Role: layout.RoleServer, Capabilities: append([]string(nil), layout.Capabilities...),
		Coordinator: "https://10.1.0.3:6444",
	}, now)
	require.NoError(t, err)
	store := &Store{Client: fake.NewSimpleClientset(), Now: func() time.Time { return now }}
	_, err = store.Bootstrap(ctx, state)
	require.NoError(t, err)
	_, encoded, err := store.CreateInvitation(ctx, layout.RoleAgent,
		[]string{layout.CapabilityApplication}, time.Hour)
	require.NoError(t, err)

	coordinator := &Coordinator{Store: store}
	tlsConfig, err := coordinator.TLSConfig(ctx)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(coordinator.Handler())
	server.TLS = tlsConfig
	server.StartTLS()
	defer server.Close()

	host := HostFacts{
		InstallationID: "worker-install",
		NodeID:         "2ec05c43-5446-47bf-b1ce-2fbce36484e5",
		Name:           "worker-1",
		Capabilities:   []string{layout.CapabilityApplication},
		AgentVersion:   "test",
	}
	client := EnrollmentClient{Endpoint: server.URL, Token: encoded}
	preflight, err := client.Preflight(ctx, host)
	require.NoError(t, err)
	require.Equal(t, "e2e", preflight.Cluster)
	require.Equal(t, layout.RoleAgent, preflight.Role)

	token, err := ParseToken(encoded)
	require.NoError(t, err)
	token.CAPin = "sha256:" + strings.Repeat("0", 64)
	body, err := json.Marshal(token)
	require.NoError(t, err)
	badPin := TokenPrefix + base64.RawURLEncoding.EncodeToString(body)
	_, err = (EnrollmentClient{Endpoint: server.URL, Token: badPin}).Preflight(ctx, host)
	require.ErrorContains(t, err, "does not match")

	_, csr, err := NewAgentKeyAndCSR(host.NodeID, host.Name)
	require.NoError(t, err)
	first, err := client.Enroll(ctx, host, csr)
	require.NoError(t, err)
	afterFirst, err := store.Load(ctx)
	require.NoError(t, err)
	candidate := afterFirst.CandidateRevision
	second, err := client.Enroll(ctx, host, csr)
	require.NoError(t, err)
	afterSecond, err := store.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, first.NodeID, second.NodeID)
	require.Equal(t, candidate, afterSecond.CandidateRevision,
		"retrying the same installation and CSR must not create a revision")
}
