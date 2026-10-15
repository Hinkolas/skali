package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/reconcile"
)

// A route's aliases reach the wire shaped like the route: each with its
// own certificate (timestamps only when set) and edge verdict, and a route
// without aliases carries no aliases key at all.
func TestStatusPayloadRouteAliases(t *testing.T) {
	t.Parallel()
	notAfter := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	checked := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	payload := newEnvironmentStatusPayload(&reconcile.Status{
		EnvironmentID: uuid.New(),
		Services: []reconcile.ServiceStatus{{Key: "web", Type: "application", Routes: []reconcile.RouteStatus{
			{Key: "public", Domain: "example.com", Path: "/", TLS: "automatic", Strategy: "round-robin",
				Certificate: &reconcile.CertificateInfo{Name: "tls-web", State: "active", NotAfter: notAfter},
				Aliases: []reconcile.RouteAliasStatus{
					{Domain: "www.example.com",
						Certificate: &reconcile.CertificateInfo{Name: "alias-tls-www", State: "issuing"},
						Edge:        &reconcile.EdgeStatus{State: "unreachable", CheckedAt: checked, Addresses: []string{"203.0.113.9: answered by another server"}, Deferred: true}},
					{Domain: "old.example.com"},
				}},
			{Key: "plain", Domain: "plain.example.com", Path: "/", TLS: "disabled", Strategy: "round-robin"},
		}}},
	})

	encoded, err := json.Marshal(payload.Services[0].Routes)
	require.NoError(t, err)
	var routes []map[string]any
	require.NoError(t, json.Unmarshal(encoded, &routes))
	require.Equal(t, "2026-12-01T00:00:00Z", routes[0]["certificate"].(map[string]any)["not_after"])
	aliases := routes[0]["aliases"].([]any)
	require.Len(t, aliases, 2)
	www := aliases[0].(map[string]any)
	require.Equal(t, "www.example.com", www["domain"])
	require.Equal(t, "alias-tls-www", www["certificate"].(map[string]any)["name"])
	require.Nil(t, www["certificate"].(map[string]any)["not_after"])
	require.Equal(t, true, www["edge"].(map[string]any)["deferred"])
	require.Equal(t, "2026-10-15T12:00:00Z", www["edge"].(map[string]any)["checked_at"])
	require.Equal(t, map[string]any{"domain": "old.example.com"}, aliases[1])
	require.NotContains(t, routes[1], "aliases")
}
