package devremote

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ParseHost validates the proxy's target: a host name or IP address
// without a port. The proxy always connects to the web ports, so a port
// in the argument would only ever be a mistake to explain.
func ParseHost(value string) (string, error) {
	host := strings.TrimSpace(value)
	if host == "" {
		return "", errors.New("pass the host the remote edge is reached at, for example mac-mini or 192.168.1.20")
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#@") {
		return "", fmt.Errorf("%q is not a host: pass a bare host name or IP address, without a scheme or path", value)
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.String(), nil
	}
	if _, port, err := net.SplitHostPort(host); err == nil {
		return "", fmt.Errorf("%q carries a port (%s); pass the host alone: the proxy always connects to ports %d and %d there",
			value, port, RemoteHTTPSPort(), RemoteHTTPPort())
	}
	if strings.ContainsAny(host, ": ") {
		return "", fmt.Errorf("%q is not a host name or IP address", value)
	}
	return strings.ToLower(strings.TrimSuffix(host, ".")), nil
}

// ParseExposeAddress validates where expose listens: an IP address of this
// machine that other machines reach it on. The wildcard is refused, because
// it would open the development edge on every network this machine joins,
// and so is loopback, which the platform's own edge already holds.
func ParseExposeAddress(value string) (netip.Addr, error) {
	text := strings.TrimSpace(value)
	if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
		text = text[1 : len(text)-1]
	}
	addr, err := netip.ParseAddr(text)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%q is not an IP address; pass the address of this machine on the network "+
			"the other machine connects from, for example its LAN or VPN address", value)
	}
	addr = addr.Unmap()
	switch {
	case addr.IsUnspecified():
		return netip.Addr{}, errors.New("refusing to expose the development edge on every interface; " +
			"pass the one address the other machine connects to, for example its LAN or VPN address")
	case addr.IsLoopback():
		return netip.Addr{}, errors.New("the development edge already listens on loopback; " +
			"pass an address other machines reach this one on, for example its LAN or VPN address")
	}
	return addr, nil
}
