// Package client is the typed REST client of the skali API, used by cmd/skali.
// It speaks the same one-shape API as every other client (web BFF, native
// apps): bearer tokens, JSON bodies, the error envelope. Payload shapes mirror
// api/openapi.yaml.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client talks to one master. Token may be empty for public endpoints.
type Client struct {
	base      string
	token     string
	userAgent string
	http      *http.Client
	// streaming has no client timeout: SSE subscriptions outlive any
	// sensible request deadline.
	streaming *http.Client
}

// Master reports the base URL this client talks to.
func (c *Client) Master() string { return c.base }

func New(master, token, userAgent string) *Client {
	transport := localhostTransport()
	return &Client{
		base:      strings.TrimRight(master, "/"),
		token:     token,
		userAgent: userAgent,
		http:      &http.Client{Timeout: 15 * time.Second, Transport: transport},
		streaming: &http.Client{Transport: transport},
	}
}

// localhostTransport pins *.localhost hosts to the loopback address: RFC
// 6761 reserves the TLD for loopback, but stub resolvers on some systems
// refuse to resolve subdomains of localhost, and the local installation
// serves skali.localhost and every app route through the loopback edge.
func localhostTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err == nil && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
			address = net.JoinHostPort("127.0.0.1", port)
		}
		return dialer.DialContext(ctx, network, address)
	}
	return transport
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
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("client: %s unreachable: %w", c.base, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("client: read response: %w", err)
	}
	if res.StatusCode >= 400 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error.Code == "" {
			return &APIError{Status: res.StatusCode, Code: "internal", Message: strings.TrimSpace(string(raw))}
		}
		return &APIError{Status: res.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
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
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("client: %s unreachable: %w", c.base, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("client: read response: %w", err)
	}

	if res.StatusCode >= 400 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error.Code == "" {
			return &APIError{Status: res.StatusCode, Code: "internal", Message: strings.TrimSpace(string(raw))}
		}
		return &APIError{Status: res.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}

	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("client: decode response: %w", err)
	}
	return nil
}
