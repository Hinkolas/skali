package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type deviceRequestResponse struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	Interval   int    `json:"interval"`
}

func (a *testAPI) startDevice(t *testing.T, path, token string) deviceRequestResponse {
	t.Helper()
	status, body := a.do("POST", path, token, map[string]string{"client_label": "skali CLI on box"})
	require.Equal(t, http.StatusCreated, status, "%v", body)
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	var res deviceRequestResponse
	require.NoError(t, json.Unmarshal(raw, &res))
	require.Len(t, res.UserCode, 9)
	require.Equal(t, 5, res.Interval)
	return res
}

func TestDeviceLoginRoundTrip(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	browser := a.login("nick@example.com", "hunter2hunter2")

	req := a.startDevice(t, "/v1/auth/device/requests", "")
	poll := map[string]string{"device_code": req.DeviceCode}

	status, body := a.do("POST", "/v1/auth/device/token", "", poll)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "pending", body["status"])
	status, body = a.do("POST", "/v1/auth/device/token", "", poll)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "slow_down", errorCode(t, body))

	// The console looks the code up in the form a person types it.
	status, body = a.do("GET", "/v1/auth/device/codes/"+req.UserCode, "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
	status, body = a.do("GET", "/v1/auth/device/codes/"+req.UserCode, browser, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "login", body["intent"])
	require.Equal(t, "skali CLI on box", body["client_label"])
	require.Equal(t, false, body["mine"])

	// Approval is a sudo action.
	a.staleAllSessions()
	status, body = a.do("POST", "/v1/auth/device/codes/"+req.UserCode+"/approve", browser, nil)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
	status, _ = a.do("POST", "/v1/auth/reauth", browser, map[string]string{"password": "hunter2hunter2"})
	require.Equal(t, http.StatusNoContent, status)
	status, body = a.do("POST", "/v1/auth/device/codes/"+req.UserCode+"/approve", browser, nil)
	require.Equal(t, http.StatusNoContent, status, "%v", body)

	// The terminal receives its session once.
	a.st.Pool.Exec(t.Context(), "UPDATE device_requests SET last_polled_at = now() - interval '10 seconds'") //nolint:errcheck
	status, body = a.do("POST", "/v1/auth/device/token", "", poll)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "approved", body["status"])
	session := body["session"].(map[string]any)
	token := session["token"].(string)
	status, body = a.do("GET", "/v1/auth/session", token, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "nick@example.com", body["user"].(map[string]any)["email"])

	status, body = a.do("POST", "/v1/auth/device/token", "", poll)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))
}

func TestDeviceReauthRoundTrip(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	a.createUser("eve@example.com", "hunter2hunter2")
	cli := a.login("nick@example.com", "hunter2hunter2")
	browser := a.login("nick@example.com", "hunter2hunter2")
	eve := a.login("eve@example.com", "hunter2hunter2")

	// The CLI session is stale; a reauth request still opens.
	a.staleAllSessions()
	status, body := a.do("POST", "/v1/auth/password", cli, map[string]string{"current_password": "hunter2hunter2", "new_password": "hunter2hunter3"})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
	req := a.startDevice(t, "/v1/auth/device/requests/reauth", cli)

	status, body = a.do("GET", "/v1/auth/device/codes/"+req.UserCode, eve, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "reauth", body["intent"])
	require.Equal(t, false, body["mine"])

	// The browser session confirms first, then approves; another user
	// cannot approve it even with a fresh session.
	status, _ = a.do("POST", "/v1/auth/reauth", eve, map[string]string{"password": "hunter2hunter2"})
	require.Equal(t, http.StatusNoContent, status)
	status, body = a.do("POST", "/v1/auth/device/codes/"+req.UserCode+"/approve", eve, nil)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "forbidden", errorCode(t, body))

	status, _ = a.do("POST", "/v1/auth/reauth", browser, map[string]string{"password": "hunter2hunter2"})
	require.Equal(t, http.StatusNoContent, status)
	status, body = a.do("GET", "/v1/auth/device/codes/"+req.UserCode, browser, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, true, body["mine"])
	status, body = a.do("POST", "/v1/auth/device/codes/"+req.UserCode+"/approve", browser, nil)
	require.Equal(t, http.StatusNoContent, status, "%v", body)

	// The CLI session is fresh again and the poll confirms without a token.
	status, body = a.do("POST", "/v1/auth/device/token", "", map[string]string{"device_code": req.DeviceCode})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "approved", body["status"])
	require.Nil(t, body["session"])
	status, body = a.do("POST", "/v1/auth/password", cli, map[string]string{"current_password": "hunter2hunter2", "new_password": "hunter2hunter3"})
	require.Equal(t, http.StatusNoContent, status, "%v", body)
}

func TestDeviceDeny(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	browser := a.login("nick@example.com", "hunter2hunter2")

	req := a.startDevice(t, "/v1/auth/device/requests", "")
	status, _ := a.do("POST", "/v1/auth/device/codes/"+req.UserCode+"/deny", browser, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, body := a.do("GET", "/v1/auth/device/codes/"+req.UserCode, browser, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))
	status, body = a.do("POST", "/v1/auth/device/token", "", map[string]string{"device_code": req.DeviceCode})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "denied", body["status"])
	status, body = a.do("POST", "/v1/auth/device/token", "", map[string]string{"device_code": "nope"})
	require.Equal(t, http.StatusNotFound, status)
	status, body = a.do("POST", "/v1/auth/device/token", "", map[string]string{})
	require.Equal(t, http.StatusBadRequest, status)
}
