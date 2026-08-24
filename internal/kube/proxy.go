package kube

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"k8s.io/client-go/rest"
)

// ServiceProxyDo performs one HTTP request against an in-cluster Service
// through the API server's service proxy, skalid's only data path into the
// pod network: the identical call works from a dev laptop (where ClusterIPs
// are unreachable) and from inside the cluster, with the API server's
// authentication on every hop. Built for the object-store ensure calls
// (internal/substrate/seaweed); anything signature-authenticated (the S3
// protocol itself) cannot ride it, the proxy prefix breaks SigV4, and never
// needs to.
//
// The URL is built by hand instead of client-go's Request.Suffix because the
// filer API is path-shaped and a trailing slash is semantic (directory
// creation), which path.Join-based helpers strip. Non-2xx answers are
// returned as data, not errors: the caller owns HTTP semantics (a 404 on a
// config read means "not there yet").
func (c *Client) ServiceProxyDo(ctx context.Context, method, namespace, service string, port int, reqPath string, query url.Values, body []byte) ([]byte, int, error) {
	client, base, err := c.proxyClient()
	if err != nil {
		return nil, 0, err
	}
	u := *base
	u.Path = "/api/v1/namespaces/" + namespace + "/services/" + service + ":" + strconv.Itoa(port) +
		"/proxy/" + strings.TrimLeft(reqPath, "/")
	u.RawQuery = query.Encode()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, 0, fmt.Errorf("kube: service proxy request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("kube: service proxy %s %s/%s:%d: %w", method, namespace, service, port, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("kube: service proxy read: %w", err)
	}
	return data, resp.StatusCode, nil
}

// NodeProxyDo performs one HTTP request against a node's kubelet through
// the API server's node proxy, with the same works-from-a-laptop property
// as ServiceProxyDo. Built for the storage sampler's /stats/summary reads.
// Non-2xx answers are returned as data, not errors: the caller owns HTTP
// semantics.
func (c *Client) NodeProxyDo(ctx context.Context, method, node, reqPath string, query url.Values, body []byte) ([]byte, int, error) {
	client, base, err := c.proxyClient()
	if err != nil {
		return nil, 0, err
	}
	u := *base
	u.Path = "/api/v1/nodes/" + node + "/proxy/" + strings.TrimLeft(reqPath, "/")
	u.RawQuery = query.Encode()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, 0, fmt.Errorf("kube: node proxy request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("kube: node proxy %s %s: %w", method, node, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("kube: node proxy read: %w", err)
	}
	return data, resp.StatusCode, nil
}

// proxyClient lazily builds the authenticated HTTP client + base URL for
// ServiceProxyDo from the same rest.Config as every other call.
func (c *Client) proxyClient() (*http.Client, *url.URL, error) {
	c.proxyOnce.Do(func() {
		c.proxyHTTP, c.proxyErr = rest.HTTPClientFor(c.Config)
		if c.proxyErr != nil {
			return
		}
		c.proxyBase, c.proxyErr = url.Parse(c.Config.Host)
	})
	if c.proxyErr != nil {
		return nil, nil, fmt.Errorf("kube: service proxy client: %w", c.proxyErr)
	}
	return c.proxyHTTP, c.proxyBase, nil
}
