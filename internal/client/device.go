package client

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Browser device authorization: the CLI side of api/openapi.yaml's
// /v1/auth/device routes.

// DeviceRequest is a pending authorization the CLI polls with DeviceCode
// while the person confirms UserCode in the console.
type DeviceRequest struct {
	DeviceCode string    `json:"device_code"`
	UserCode   string    `json:"user_code"`
	ExpiresAt  time.Time `json:"expires_at"`
	Interval   int       `json:"interval"`
}

// DevicePoll is one poll's answer; Session is set once, on the poll that
// observes an approved login request.
type DevicePoll struct {
	Status  string          `json:"status"`
	Session *SessionCreated `json:"session"`
}

// Device poll states.
const (
	DevicePending  = "pending"
	DeviceApproved = "approved"
	DeviceDenied   = "denied"
	DeviceExpired  = "expired"
)

// StartDeviceLogin opens an anonymous login request; label is shown to
// the approver.
func (c *Client) StartDeviceLogin(ctx context.Context, label string) (*DeviceRequest, error) {
	var res DeviceRequest
	if err := c.do(ctx, http.MethodPost, "/v1/auth/device/requests", map[string]string{"client_label": label}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// StartDeviceReauth opens a request bound to this client's session; its
// approval refreshes the session's sudo window.
func (c *Client) StartDeviceReauth(ctx context.Context, label string) (*DeviceRequest, error) {
	var res DeviceRequest
	if err := c.do(ctx, http.MethodPost, "/v1/auth/device/requests/reauth", map[string]string{"client_label": label}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// PollDevice asks for the request's state. Terminal states are reported
// once; afterwards the code answers not_found.
func (c *Client) PollDevice(ctx context.Context, deviceCode string) (*DevicePoll, error) {
	var res DevicePoll
	if err := c.do(ctx, http.MethodPost, "/v1/auth/device/token", map[string]string{"device_code": deviceCode}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// IsSlowDown reports the server asking the poller to back off.
func IsSlowDown(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == "slow_down"
}

// IsNotFound reports a 404 from the server, which for device routes also
// means an older daemon without the device flow.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}
