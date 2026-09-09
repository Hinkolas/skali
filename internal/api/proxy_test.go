package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForwardedClientAddress(t *testing.T) {
	trust := func(ip netip.Addr) bool { return netip.MustParsePrefix("10.0.0.0/24").Contains(ip) }
	for _, tc := range []struct{ name, peer, real, forwarded, want string }{
		{"direct forged real", "192.0.2.1:123", "198.51.100.1", "", "192.0.2.1"},
		{"direct forged chain", "192.0.2.1:123", "", "198.51.100.1", "192.0.2.1"},
		{"proxy real", "10.0.0.1:123", "198.51.100.1", "", "198.51.100.1"},
		{"forged leftmost", "10.0.0.1:123", "203.0.113.1", "203.0.113.1, 198.51.100.1", "198.51.100.1"},
		{"proxy chain", "10.0.0.1:123", "", "198.51.100.1, 10.0.0.2", "198.51.100.1"},
		{"invalid real", "10.0.0.1:123", "not-an-ip", "", "10.0.0.1"},
		{"invalid chain", "10.0.0.1:123", "198.51.100.1", "garbage, 10.0.0.2", "10.0.0.1"},
		{"IPv6 client", "10.0.0.1:123", "", "2001:db8::1", "2001:db8::1"},
		{"mapped peer", "[::ffff:10.0.0.1]:123", "198.51.100.1", "", "198.51.100.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.peer
			if tc.real != "" {
				r.Header.Set("X-Real-IP", tc.real)
			}
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			h := realIP(trust)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				require.Equal(t, tc.want, clientIP(r))
			}))
			h.ServeHTTP(httptest.NewRecorder(), r)
		})
	}
}

func TestForwardedHeadersUntrustedByDefault(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:123"
	r.Header.Set("X-Real-IP", "198.51.100.1")
	realIP(nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		require.Equal(t, "127.0.0.1", clientIP(r))
	})).ServeHTTP(httptest.NewRecorder(), r)
}
