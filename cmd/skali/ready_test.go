package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
)

func TestRouteURL(t *testing.T) {
	t.Parallel()
	active := &client.CertificateStatus{State: "active"}
	local := edgePorts{HTTP: 80, HTTPS: 443}
	shifted := edgePorts{HTTP: 8082, HTTPS: 8443}
	cases := []struct {
		name  string
		route client.RouteStatus
		ports edgePorts
		want  string
	}{
		{"local route serves https on the default port", client.RouteStatus{Domain: "app.localhost", Path: "/", Certificate: active}, local, "https://app.localhost"},
		{"local route with a path", client.RouteStatus{Domain: "app.localhost", Path: "/api", Certificate: active}, local, "https://app.localhost/api"},
		{"local route that opted out of tls", client.RouteStatus{Domain: "app.localhost", Path: "/"}, local, "http://app.localhost"},
		{"shifted edge names the https port", client.RouteStatus{Domain: "app.localhost", Path: "/", Certificate: active}, shifted, "https://app.localhost:8443"},
		{"shifted edge names the http port", client.RouteStatus{Domain: "app.localhost", Path: "/api"}, shifted, "http://app.localhost:8082/api"},
		{"remote without a certificate", client.RouteStatus{Domain: "app.example.com"}, edgePorts{}, "http://app.example.com"},
		{"remote with a certificate", client.RouteStatus{Domain: "app.example.com", Certificate: active}, edgePorts{}, "https://app.example.com"},
		{"deferred route keeps https", client.RouteStatus{Domain: "app.example.com",
			Certificate: &client.CertificateStatus{State: "pending"}, Edge: &client.EdgeStatus{State: "unreachable"}}, edgePorts{}, "https://app.example.com"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, routeURL(c.route, c.ports), c.name)
	}
}

func TestReadySummaryLines(t *testing.T) {
	t.Parallel()
	style := clirender.StyleFor(&bytes.Buffer{})
	active := &client.CertificateStatus{State: "active"}
	status := &client.EnvironmentStatus{Services: []client.ServiceStatus{
		{Key: "data", Type: "database"},
		{Key: "api", Type: "application", Routes: []client.RouteStatus{
			{Key: "public", Domain: "api.app.localhost", Path: "/", Certificate: active},
			{Key: "graph", Domain: "api.app.localhost", Path: "/graphql", Certificate: active},
		}},
		{Key: "web", Type: "application", Routes: []client.RouteStatus{
			{Key: "public", Domain: "app.localhost", Path: "/", Certificate: active},
		}},
		{Key: "worker", Type: "application"},
	}}

	t.Run("dev summary", func(t *testing.T) {
		t.Parallel()
		lines := readySummaryLines(style, status, readySummary{
			Dashboard: "https://skali.localhost",
			Edge:      edgePorts{HTTP: 80, HTTPS: 443},
			DevPorts: map[string]map[string]int{
				"web":    {"http": 21521},
				"worker": {"http": 21530, "metrics": 21531},
			},
		})
		require.Equal(t, []string{
			"  dashboard  https://skali.localhost",
			"  api        https://api.app.localhost",
			"             https://api.app.localhost/graphql",
			"  web        https://app.localhost  -> dev process on localhost:21521",
			"  worker     -> dev process on http=localhost:21530, metrics=localhost:21531",
		}, lines)
	})

	t.Run("dev summary on a shifted edge names the ports", func(t *testing.T) {
		t.Parallel()
		lines := readySummaryLines(style, status, readySummary{
			Dashboard: "https://skali.localhost:8443",
			Edge:      edgePorts{HTTP: 8082, HTTPS: 8443},
		})
		require.Equal(t, []string{
			"  dashboard  https://skali.localhost:8443",
			"  api        https://api.app.localhost:8443",
			"             https://api.app.localhost:8443/graphql",
			"  web        https://app.localhost:8443",
		}, lines)
	})

	t.Run("remote summary with a pending certificate", func(t *testing.T) {
		t.Parallel()
		remote := &client.EnvironmentStatus{Services: []client.ServiceStatus{
			{Key: "web", Type: "application", Routes: []client.RouteStatus{
				{Key: "public", Domain: "app.example.com", Path: "/",
					Certificate: &client.CertificateStatus{State: "issuing", Reason: "Pending"}},
			}},
		}}
		lines := readySummaryLines(style, remote, readySummary{})
		require.Equal(t, []string{
			"  web  https://app.example.com  cert issuing (Pending)",
		}, lines)
	})

	t.Run("remote summary with a deferred route", func(t *testing.T) {
		t.Parallel()
		remote := &client.EnvironmentStatus{Services: []client.ServiceStatus{
			{Key: "web", Type: "application", Routes: []client.RouteStatus{
				{Key: "public", Domain: "shop.example.com", Path: "/",
					Certificate: &client.CertificateStatus{State: "issuing", Reason: "Pending"},
					Edge:        &client.EdgeStatus{State: "unreachable", Message: "no address answers as this installation", Deferred: true}},
			}},
		}}
		lines := readySummaryLines(style, remote, readySummary{})
		require.Equal(t, []string{
			"  web  https://shop.example.com  cert deferred · domain not pointing here yet",
		}, lines)
	})

	t.Run("an unprobed edge falls back to the certificate state", func(t *testing.T) {
		t.Parallel()
		remote := &client.EnvironmentStatus{Services: []client.ServiceStatus{
			{Key: "web", Type: "application", Routes: []client.RouteStatus{
				{Key: "public", Domain: "app.example.com", Path: "/",
					Certificate: &client.CertificateStatus{State: "issuing", Reason: "Pending"},
					Edge:        &client.EdgeStatus{State: "unknown"}},
			}},
		}}
		lines := readySummaryLines(style, remote, readySummary{})
		require.Equal(t, []string{
			"  web  https://app.example.com  cert issuing (Pending)",
		}, lines)
	})

	t.Run("nothing to show prints nothing", func(t *testing.T) {
		t.Parallel()
		bare := &client.EnvironmentStatus{Services: []client.ServiceStatus{
			{Key: "worker", Type: "application"},
		}}
		require.Empty(t, readySummaryLines(style, bare, readySummary{}))
	})

	t.Run("a bypassed protection leads the summary", func(t *testing.T) {
		t.Parallel()
		remote := &client.EnvironmentStatus{Services: []client.ServiceStatus{
			{Key: "web", Type: "application", Routes: []client.RouteStatus{
				{Key: "public", Domain: "app.example.com", Path: "/"},
			}},
		}}
		lines := readySummaryLines(style, remote, readySummary{ProtectionBypassed: true})
		require.Equal(t, []string{
			"  protection  bypassed (recorded on the run)",
			"  web         http://app.example.com",
		}, lines)
	})

	t.Run("intercepted application missing from status still lists its port", func(t *testing.T) {
		t.Parallel()
		lines := readySummaryLines(style, &client.EnvironmentStatus{}, readySummary{
			DevPorts: map[string]map[string]int{"web": {"http": 21521}},
		})
		require.Len(t, lines, 1)
		require.True(t, strings.HasSuffix(lines[0], "-> dev process on localhost:21521"), lines[0])
	})
}

func TestReadyWarningLines(t *testing.T) {
	t.Parallel()
	style := clirender.StyleFor(&bytes.Buffer{})
	deferred := &client.EdgeStatus{State: "partial", Message: "AAAA record still points elsewhere", Deferred: true}
	status := &client.EnvironmentStatus{Services: []client.ServiceStatus{
		{Key: "web", Type: "application", Routes: []client.RouteStatus{
			{Key: "public", Domain: "shop.example.com", Path: "/", Certificate: &client.CertificateStatus{State: "pending"}, Edge: deferred},
			{Key: "api", Domain: "shop.example.com", Path: "/api", Certificate: &client.CertificateStatus{State: "pending"}, Edge: deferred},
			{Key: "admin", Domain: "admin.example.com", Path: "/", Certificate: &client.CertificateStatus{State: "active"},
				Edge: &client.EdgeStatus{State: "reachable"}},
		}},
	}}
	require.Equal(t, []string{
		"warning: shop.example.com does not reach this installation yet (AAAA record still points elsewhere); TLS is issued automatically once its DNS points here",
	}, readyWarningLines(style, status), "one warning per domain, none for a reachable route")

	// A verdict the reconciler no longer calls deferred (the certificate
	// went active), and a route never probed, warn about nothing.
	settled := &client.EdgeStatus{State: "partial", Message: "AAAA record still points elsewhere"}
	quiet := &client.EnvironmentStatus{Services: []client.ServiceStatus{
		{Key: "web", Type: "application", Routes: []client.RouteStatus{
			{Key: "public", Domain: "shop.example.com", Certificate: &client.CertificateStatus{State: "active"}, Edge: settled},
			{Key: "api", Domain: "api.example.com", Certificate: &client.CertificateStatus{State: "pending"}},
		}},
	}}
	require.Empty(t, readyWarningLines(style, quiet))
}
