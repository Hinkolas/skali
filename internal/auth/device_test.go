package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUserCodeFormat(t *testing.T) {
	code, err := newUserCode()
	require.NoError(t, err)
	require.Len(t, code, userCodeLength)
	for _, r := range code {
		require.Contains(t, userCodeAlphabet, string(r))
	}
	require.Equal(t, code[:4]+"-"+code[4:], FormatUserCode(code))

	got, ok := NormalizeUserCode(" bcdf-2345 ")
	require.True(t, ok)
	require.Equal(t, "BCDF2345", got)
	_, ok = NormalizeUserCode("ABCD-2345") // A is not in the alphabet
	require.False(t, ok)
	_, ok = NormalizeUserCode("BCDF-234")
	require.False(t, ok)

	require.Equal(t, "skali CLI", sanitizeDeviceLabel("  \x00 "))
	require.Equal(t, "skali CLI on box", sanitizeDeviceLabel("skali CLI on box\n"))
}

func TestDeviceLoginFlow(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	user, err := CreateUser(ctx, st, "nick@example.com", "Nick", "hunter2hunter2", RoleMember)
	require.NoError(t, err)

	cliMeta := SessionMeta{IPAddress: "198.51.100.9", UserAgent: "skali-cli"}
	req, err := svc.StartDeviceLogin(ctx, "skali CLI on box", cliMeta)
	require.NoError(t, err)
	require.Len(t, req.DeviceCode, 43)
	require.Len(t, req.UserCode, 9)
	require.Equal(t, DevicePollInterval, req.Interval)

	// Pending while nobody has approved.
	poll, err := svc.PollDevice(ctx, req.DeviceCode, cliMeta.IPAddress)
	require.NoError(t, err)
	require.Equal(t, DevicePending, poll.Status)
	require.Nil(t, poll.Session)

	// Polling again immediately is refused without changing anything.
	_, err = svc.PollDevice(ctx, req.DeviceCode, cliMeta.IPAddress)
	require.ErrorIs(t, err, ErrSlowDown)

	// The browser sees the label; the request is not "mine" for a login.
	info, err := svc.LookupDevice(ctx, user, req.UserCode)
	require.NoError(t, err)
	require.Equal(t, DeviceLogin, info.Intent)
	require.Equal(t, "skali CLI on box", info.ClientLabel)
	require.False(t, info.Mine)

	require.NoError(t, svc.ApproveDevice(ctx, user, req.UserCode))
	// Approved requests are no longer pending for the browser.
	_, err = svc.LookupDevice(ctx, user, req.UserCode)
	require.ErrorIs(t, err, ErrDeviceNotFound)

	// The next poll mints the session with the terminal's identity, once.
	base := time.Now()
	svc.now = func() time.Time { return base.Add(DevicePollInterval) }
	poll, err = svc.PollDevice(ctx, req.DeviceCode, cliMeta.IPAddress)
	require.NoError(t, err)
	require.Equal(t, DeviceApproved, poll.Status)
	require.NotNil(t, poll.Session)
	require.Equal(t, user.ID, poll.Session.User.ID)
	gotUser, gotSess, err := svc.Authenticate(ctx, poll.Session.Token)
	require.NoError(t, err)
	require.Equal(t, user.ID, gotUser.ID)
	require.Equal(t, cliMeta.IPAddress, gotSess.IpAddress)
	require.Equal(t, cliMeta.UserAgent, gotSess.UserAgent)

	_, err = svc.PollDevice(ctx, req.DeviceCode, cliMeta.IPAddress)
	require.ErrorIs(t, err, ErrDeviceNotFound)
}

func TestDeviceDenyAndExpiry(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	user, err := CreateUser(ctx, st, "nick@example.com", "Nick", "hunter2hunter2", RoleMember)
	require.NoError(t, err)

	req, err := svc.StartDeviceLogin(ctx, "cli", meta)
	require.NoError(t, err)
	require.NoError(t, svc.DenyDevice(ctx, user, req.UserCode))
	require.ErrorIs(t, svc.ApproveDevice(ctx, user, req.UserCode), ErrDeviceNotFound)
	poll, err := svc.PollDevice(ctx, req.DeviceCode, meta.IPAddress)
	require.NoError(t, err)
	require.Equal(t, DeviceDenied, poll.Status)
	_, err = svc.PollDevice(ctx, req.DeviceCode, meta.IPAddress)
	require.ErrorIs(t, err, ErrDeviceNotFound)

	// Expiry: the poll reports it once and sweeps the row.
	req, err = svc.StartDeviceLogin(ctx, "cli", meta)
	require.NoError(t, err)
	base := time.Now()
	svc.now = func() time.Time { return base.Add(defaultDeviceTTL + time.Minute) }
	_, err = svc.LookupDevice(ctx, user, req.UserCode)
	require.ErrorIs(t, err, ErrDeviceNotFound)
	poll, err = svc.PollDevice(ctx, req.DeviceCode, meta.IPAddress)
	require.NoError(t, err)
	require.Equal(t, DeviceExpired, poll.Status)
	_, err = svc.PollDevice(ctx, req.DeviceCode, meta.IPAddress)
	require.ErrorIs(t, err, ErrDeviceNotFound)

	// SweepExpired clears leftovers nobody polled.
	svc.now = time.Now
	_, err = svc.StartDeviceLogin(ctx, "cli", meta)
	require.NoError(t, err)
	svc.now = func() time.Time { return base.Add(defaultDeviceTTL + time.Minute) }
	require.NoError(t, svc.SweepExpired(ctx))
	n, err := st.DeleteExpiredDeviceRequests(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestDeviceReauthFlow(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	owner, err := CreateUser(ctx, st, "nick@example.com", "Nick", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	other, err := CreateUser(ctx, st, "eve@example.com", "Eve", "hunter2hunter2", RoleMember)
	require.NoError(t, err)

	base := time.Now()
	svc.now = func() time.Time { return base }
	login, err := svc.Login(ctx, owner.Email, "hunter2hunter2", meta)
	require.NoError(t, err)
	cliSession := login.Session

	// The CLI session ages past the sudo window.
	svc.now = func() time.Time { return base.Add(16 * time.Minute) }
	_, sess, err := svc.Authenticate(ctx, cliSession.Token)
	require.NoError(t, err)
	require.False(t, svc.IsSessionFresh(sess))

	req, err := svc.StartDeviceReauth(ctx, owner, cliSession.ID, "skali CLI on box", meta)
	require.NoError(t, err)

	// Another user sees it is not theirs and cannot approve it.
	info, err := svc.LookupDevice(ctx, other, req.UserCode)
	require.NoError(t, err)
	require.Equal(t, DeviceReauth, info.Intent)
	require.False(t, info.Mine)
	require.ErrorIs(t, svc.ApproveDevice(ctx, other, req.UserCode), ErrDeviceForeign)

	info, err = svc.LookupDevice(ctx, owner, req.UserCode)
	require.NoError(t, err)
	require.True(t, info.Mine)
	require.NoError(t, svc.ApproveDevice(ctx, owner, req.UserCode))

	// The bound session is fresh again; approval minted nothing.
	_, sess, err = svc.Authenticate(ctx, cliSession.Token)
	require.NoError(t, err)
	require.True(t, svc.IsSessionFresh(sess))
	poll, err := svc.PollDevice(ctx, req.DeviceCode, meta.IPAddress)
	require.NoError(t, err)
	require.Equal(t, DeviceApproved, poll.Status)
	require.Nil(t, poll.Session)

	// Revoking the CLI session takes its pending request with it.
	req, err = svc.StartDeviceReauth(ctx, owner, cliSession.ID, "cli", meta)
	require.NoError(t, err)
	require.NoError(t, svc.Logout(ctx, cliSession.Token))
	_, err = svc.LookupDevice(ctx, owner, req.UserCode)
	require.ErrorIs(t, err, ErrDeviceNotFound)
}
