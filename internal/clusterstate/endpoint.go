package clusterstate

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// NormalizeEndpoint accepts the human-friendly forms used by cluster join
// and returns one strict HTTPS origin for the coordinator.
func NormalizeEndpoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("coordinator endpoint is required")
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid coordinator endpoint %q: %w", value, err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("coordinator endpoint must use HTTPS, got %q", parsed.Scheme)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("coordinator endpoint must be an HTTPS origin without credentials, path, query, or fragment")
	}
	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("coordinator endpoint has no host")
	}
	port := parsed.Port()
	if port == "" {
		port = DefaultCoordinatorPort
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return "", fmt.Errorf("coordinator endpoint port %q is invalid", port)
	}
	parsed.Host = net.JoinHostPort(host, port)
	parsed.Path = ""
	return parsed.String(), nil
}
