package clusterstate

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Hinkolas/skali/internal/layout"
)

func TestInvitationOneUseAndCSRBoundRetry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	store := &Store{Client: fake.NewSimpleClientset(), Now: func() time.Time { return now }}
	state, err := NewSeedState("test", Node{
		ID: "seed-id", InstallationID: "seed-install", Name: "seed",
		Role: layout.RoleServer, Capabilities: append([]string(nil), layout.Capabilities...),
	}, now)
	require.NoError(t, err)
	_, err = store.Bootstrap(ctx, state)
	require.NoError(t, err)

	_, encoded, err := store.CreateInvitation(ctx, layout.RoleServer,
		[]string{layout.CapabilityApplication}, time.Hour)
	require.NoError(t, err)
	token, err := ParseToken(encoded)
	require.NoError(t, err)
	caps := []string{layout.CapabilityApplication}

	invitation, err := store.BindInvitation(ctx, token, "install-a", caps, []byte("csr-a"))
	require.NoError(t, err)
	require.Equal(t, "install-a", invitation.UsedBy)
	_, err = store.BindInvitation(ctx, token, "install-a", caps, []byte("csr-a"))
	require.NoError(t, err)
	_, err = store.BindInvitation(ctx, token, "install-a", caps, []byte("csr-b"))
	require.ErrorContains(t, err, "different CSR")
	_, err = store.BindInvitation(ctx, token, "install-b", caps, []byte("csr-a"))
	require.ErrorContains(t, err, "another host")
}

func TestInvitationExpiryAndRevocation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	store := &Store{Client: fake.NewSimpleClientset(), Now: func() time.Time { return now }}
	state, err := NewSeedState("test", Node{
		ID: "seed-id", InstallationID: "seed-install", Name: "seed",
		Role: layout.RoleServer, Capabilities: append([]string(nil), layout.Capabilities...),
	}, now)
	require.NoError(t, err)
	_, err = store.Bootstrap(ctx, state)
	require.NoError(t, err)

	invitation, encoded, err := store.CreateInvitation(ctx, layout.RoleAgent,
		[]string{layout.CapabilityApplication}, time.Hour)
	require.NoError(t, err)
	token, err := ParseToken(encoded)
	require.NoError(t, err)
	require.NoError(t, store.RevokeInvitation(ctx, invitation.ID))
	_, err = store.CheckInvitation(ctx, token, []string{layout.CapabilityApplication})
	require.ErrorContains(t, err, "revoked")

	_, encoded, err = store.CreateInvitation(ctx, layout.RoleAgent,
		[]string{layout.CapabilityApplication}, time.Hour)
	require.NoError(t, err)
	token, err = ParseToken(encoded)
	require.NoError(t, err)
	now = now.Add(2 * time.Hour)
	_, err = store.CheckInvitation(ctx, token, []string{layout.CapabilityApplication})
	require.ErrorContains(t, err, "expired")
}
