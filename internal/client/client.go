// Package client is the typed REST client of the skali API, used by cmd/skali.
// It speaks the same one-shape API as every other client (web console, native
// apps): bearer tokens, JSON bodies, the error envelope. Payload shapes mirror
// api/openapi.yaml.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// InstanceHeader is the response header every skalid stamps with its
// installation identity, minted when the cluster's database was created.
// Pinning it per remote detects an uninstalled-and-reinstalled cluster,
// which would otherwise surface as a confusing expired session (or, after
// re-onboarding with the same credentials, as missing projects). It is a
// convenience signal, not MITM protection: server authentication is TLS's
// job.
const InstanceHeader = "Skali-Instance"

// VersionHeader is the response header carrying the daemon's build version,
// available pre-auth and on error responses (the meta endpoint needs a
// session). The API is a private wire built from one commit: a released
// daemon answers CodeCLIVersionMismatch to a released CLI of another
// version, and cmd/skali turns the observed version into the upgrade hint.
// Development builds on either side (v0.0.0-dev, git describe) are never
// compared.
const VersionHeader = "Skali-Version"

// ClientVersionHeader is the request header carrying this client's build
// version on every request, the daemon's side of the exact-match contract.
const ClientVersionHeader = "Skali-Client-Version"

// CodeCLIVersionMismatch is the error code a released daemon answers when
// this CLI is another release; the required version is the response's
// VersionHeader.
const CodeCLIVersionMismatch = "cli_version_mismatch"

// Caller identifies the program behind a client: UserAgent lands in session
// lists, Version rides ClientVersionHeader for the daemon's exact-match
// gate. Either may be empty, which sends no header.
type Caller struct {
	UserAgent string
	Version   string
	// RootCAs holds PEM certificates trusted in addition to the system
	// roots: the local platform's development CA, which signs every
	// *.localhost certificate the loopback edge serves. Empty keeps the
	// system roots alone.
	RootCAs []byte
}

// Client talks to one master. Token may be empty for public endpoints.
type Client struct {
	base   string
	token  string
	caller Caller
	http   *http.Client
	// streaming has no client timeout: SSE subscriptions outlive any
	// sensible request deadline.
	streaming *http.Client
	// tls is the verification config every transport shares (nil for the
	// system defaults); the exec WebSocket dialer reuses it.
	tls *tls.Config

	// Install-identity pinning and version observation state; see
	// PinInstance and OnVersion.
	mu              sync.Mutex
	pinned          string
	observed        string
	observedVersion string
	onAdopt         func(observed string)
	onVersion       func(observed string)
}

// Master reports the base URL this client talks to.
func (c *Client) Master() string { return c.base }

func New(master, token string, caller Caller) *Client {
	tlsConfig := tlsConfigWithRoots(caller.RootCAs)
	transport := localhostTransport(tlsConfig)
	return &Client{
		base:      strings.TrimRight(master, "/"),
		token:     token,
		caller:    caller,
		http:      &http.Client{Timeout: 15 * time.Second, Transport: transport},
		streaming: &http.Client{Transport: transport},
		tls:       tlsConfig,
	}
}

// tlsConfigWithRoots trusts the given PEM roots on top of the system roots,
// or returns nil (the system defaults) when there are none. The pool
// starts from the system pool, which on macOS and Windows still defers to
// the platform verifier for roots it does not hold itself, so a client
// carrying the local development CA keeps verifying public remotes.
func tlsConfigWithRoots(rootCAs []byte) *tls.Config {
	if len(rootCAs) == 0 {
		return nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(rootCAs) {
		return nil
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// stamp sets the headers every request carries: the session, the caller
// identity, and the caller version the daemon gates on. All four request
// builders (JSON, health, SSE, exec) go through it.
func (c *Client) stamp(h http.Header) {
	if c.token != "" {
		h.Set("Authorization", "Bearer "+c.token)
	}
	if c.caller.UserAgent != "" {
		h.Set("User-Agent", c.caller.UserAgent)
	}
	if c.caller.Version != "" {
		h.Set(ClientVersionHeader, c.caller.Version)
	}
}

// localhostTransport pins *.localhost hosts to the loopback address: RFC
// 6761 reserves the TLD for loopback, but stub resolvers on some systems
// refuse to resolve subdomains of localhost, and the local installation
// serves skali.localhost and every app route through the loopback TLS
// edge. The dial keeps the URL's host as SNI, so the edge's per-host
// certificates verify against the trusted roots.
func localhostTransport(tlsConfig *tls.Config) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = localhostDialContext()
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	return transport
}

// localhostDialContext is the dial function behind localhostTransport,
// shared with the exec WebSocket dialer so both transports resolve
// *.localhost identically.
func localhostDialContext() func(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err == nil && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
			address = net.JoinHostPort("127.0.0.1", port)
		}
		return dialer.DialContext(ctx, network, address)
	}
}

// PinInstance arms install-identity verification. Every response carrying
// the Skali-Instance header is compared against pinned; a mismatch fails the
// request with *InstanceMismatchError, taking precedence over the response
// itself (the identity change explains whatever error rode along). An empty
// pinned value means trust on first use: the first observed identity is
// adopted and reported through onAdopt so the caller can persist it. A
// response without the header (an older daemon) verifies nothing.
func (c *Client) PinInstance(pinned string, onAdopt func(observed string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pinned = pinned
	c.onAdopt = onAdopt
}

// ObservedInstance reports the identity from the most recent response, empty
// until one carried the header. Callers that must not fail on a mismatch
// (probes ahead of an interactive trust decision) read it off an unpinned
// client.
func (c *Client) ObservedInstance() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.observed
}

// ObservedVersion reports the daemon build version from the most recent
// response, empty until one carried the header (an older daemon).
func (c *Client) ObservedVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.observedVersion
}

// OnVersion registers a callback for the daemon version: it fires the first
// time a response carries VersionHeader and again whenever the value
// changes, so callers can act on skew without polling ObservedVersion. It
// fires on error responses too, including the daemon's own version refusal.
func (c *Client) OnVersion(fn func(observed string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onVersion = fn
}

// checkInstance records the response's platform headers and enforces the
// install-identity pin before publishing version observations. A response
// from a different installation cannot change the selected release.
func (c *Client) checkInstance(res *http.Response) error {
	observed, release := res.Header.Get(InstanceHeader), res.Header.Get(VersionHeader)
	c.mu.Lock()
	pinned, adopt, notify := c.pinned, c.onAdopt, c.onVersion
	if observed != "" {
		c.observed = observed
	}
	if observed != "" && pinned != "" && observed != pinned {
		c.mu.Unlock()
		return &InstanceMismatchError{Master: c.base, Pinned: pinned, Observed: observed}
	}
	if observed != "" && pinned == "" {
		c.pinned = observed
	}
	changed := release != "" && release != c.observedVersion
	if release != "" {
		c.observedVersion = release
	}
	c.mu.Unlock()
	if changed && notify != nil {
		notify(release)
	}
	if observed != "" && pinned == "" && adopt != nil {
		adopt(observed)
	}
	return nil
}

// InstanceMismatchError reports that the master answered as a different
// installation than the one pinned for it, which almost always means the
// cluster was uninstalled and reinstalled.
type InstanceMismatchError struct {
	Master   string
	Pinned   string
	Observed string
}

func (e *InstanceMismatchError) Error() string {
	return fmt.Sprintf("the server at %s identifies as installation %s, but this remote is pinned to %s; the cluster was probably reinstalled",
		e.Master, shortInstance(e.Observed), shortInstance(e.Pinned))
}

// shortInstance abbreviates an installation id for error messages; the full
// values stay on the error's fields.
func shortInstance(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// decodeErrorEnvelope turns a non-2xx body into an *APIError.
func decodeErrorEnvelope(status int, raw []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error.Code == "" {
		return &APIError{Status: status, Code: "internal", Message: strings.TrimSpace(string(raw))}
	}
	return &APIError{Status: status, Code: envelope.Error.Code, Message: envelope.Error.Message}
}

// APIError is a decoded error envelope. Callers branch on Code.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s (%s)", e.Message, e.Code)
}

// --- payload shapes (mirror api/openapi.yaml) ---

type User struct {
	ID               string    `json:"id"`
	Email            string    `json:"email"`
	Name             string    `json:"name"`
	Role             string    `json:"role"`
	CreateProjects   bool      `json:"create_projects"`
	TwoFactorEnabled bool      `json:"two_factor_enabled"`
	CreatedAt        time.Time `json:"created_at"`
}

type SessionCreated struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      User      `json:"user"`
}

type Challenge struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LoginResult has exactly one field set, mirroring the login response.
type LoginResult struct {
	Session   *SessionCreated `json:"session"`
	Challenge *Challenge      `json:"challenge"`
}

type Session struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
	IPAddress string    `json:"ip_address"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	Current   bool      `json:"current"`
}

type SessionInfo struct {
	User    User    `json:"user"`
	Session Session `json:"session"`
}

// --- endpoints ---

func (c *Client) Login(ctx context.Context, email, password string) (*LoginResult, error) {
	var res LoginResult
	err := c.do(ctx, http.MethodPost, "/v1/auth/login",
		map[string]string{"email": email, "password": password}, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) VerifyTwoFactor(ctx context.Context, challengeToken, code string) (*SessionCreated, error) {
	var res struct {
		Session SessionCreated `json:"session"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/auth/2fa/verify",
		map[string]string{"challenge_token": challengeToken, "code": code}, &res)
	if err != nil {
		return nil, err
	}
	return &res.Session, nil
}

func (c *Client) Logout(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/auth/logout", nil, nil)
}

func (c *Client) CurrentSession(ctx context.Context) (*SessionInfo, error) {
	var res SessionInfo
	if err := c.do(ctx, http.MethodGet, "/v1/auth/session", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	var res struct {
		Sessions []Session `json:"sessions"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/auth/sessions", nil, &res); err != nil {
		return nil, err
	}
	return res.Sessions, nil
}

func (c *Client) RevokeSession(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/auth/sessions/"+id, nil, nil)
}

// Health probes the daemon and requires its JSON answer. On a
// single-surface cluster the domain root serves the web console, whose
// /healthz is plain text, so a base URL missing the /api path must fail
// here with guidance instead of passing and confusing the login after it.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/healthz", nil)
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}
	c.stamp(req.Header)
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("client: %s unreachable: %w", c.base, err)
	}
	defer res.Body.Close()
	if err := c.checkInstance(res); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("client: read response: %w", err)
	}
	if res.StatusCode >= 400 {
		return decodeErrorEnvelope(res.StatusCode, raw)
	}
	var body struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Status != "ok" {
		return fmt.Errorf("client: %s answered, but not like a skali api; "+
			"a cluster serves its api at https://<domain>/api", c.base)
	}
	return nil
}

// do sends one JSON request and decodes either the response into out or the
// error envelope into an *APIError.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("client: encode request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reqBody)
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.stamp(req.Header)

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("client: %s unreachable: %w", c.base, err)
	}
	defer res.Body.Close()

	if err := c.checkInstance(res); err != nil {
		return err
	}

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("client: read response: %w", err)
	}

	if res.StatusCode >= 400 {
		return decodeErrorEnvelope(res.StatusCode, raw)
	}

	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("client: decode response: %w", err)
	}
	return nil
}
