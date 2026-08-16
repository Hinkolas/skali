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

	t.Run("nothing to show prints nothing", func(t *testing.T) {
		t.Parallel()
		bare := &client.EnvironmentStatus{Services: []client.ServiceStatus{
			{Key: "worker", Type: "application"},
		}}
		require.Empty(t, readySummaryLines(style, bare, readySummary{}))
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
