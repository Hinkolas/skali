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
	cases := []struct {
		name  string
		route client.RouteStatus
		port  int
		want  string
	}{
		{"local plain route on the edge port", client.RouteStatus{Domain: "app.localhost", Path: "/"}, 8080, "http://app.localhost:8080"},
		{"local route with a path", client.RouteStatus{Domain: "app.localhost", Path: "/api"}, 8080, "http://app.localhost:8080/api"},
		{"remote without a certificate", client.RouteStatus{Domain: "app.example.com"}, 0, "http://app.example.com"},
		{"remote with a certificate", client.RouteStatus{Domain: "app.example.com", Certificate: active}, 0, "https://app.example.com"},
		{"default http port stays implicit", client.RouteStatus{Domain: "app.example.com"}, 80, "http://app.example.com"},
		{"deferred route keeps https", client.RouteStatus{Domain: "app.example.com",
			Certificate: &client.CertificateStatus{State: "pending"}, Edge: &client.EdgeStatus{State: "unreachable"}}, 0, "https://app.example.com"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, routeURL(c.route, c.port), c.name)
	}
}

func TestReadySummaryLines(t *testing.T) {
	t.Parallel()
	style := clirender.StyleFor(&bytes.Buffer{})
	status := &client.EnvironmentStatus{Services: []client.ServiceStatus{
		{Key: "data", Type: "database"},
		{Key: "api", Type: "application", Routes: []client.RouteStatus{
			{Key: "public", Domain: "api.app.localhost", Path: "/"},
			{Key: "graph", Domain: "api.app.localhost", Path: "/graphql"},
		}},
		{Key: "web", Type: "application", Routes: []client.RouteStatus{
			{Key: "public", Domain: "app.localhost", Path: "/"},
		}},
		{Key: "worker", Type: "application"},
	}}

	t.Run("dev summary", func(t *testing.T) {
		t.Parallel()
		lines := readySummaryLines(style, status, readySummary{
			Dashboard: "http://skali.localhost:8080",
			HTTPPort:  8080,
			DevPorts: map[string]map[string]int{
				"web":    {"http": 21521},
				"worker": {"http": 21530, "metrics": 21531},
			},
		})
		require.Equal(t, []string{
			"  dashboard  http://skali.localhost:8080",
			"  api        http://api.app.localhost:8080",
			"             http://api.app.localhost:8080/graphql",
			"  web        http://app.localhost:8080  -> dev process on localhost:21521",
			"  worker     -> dev process on http=localhost:21530, metrics=localhost:21531",
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
