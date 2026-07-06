package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st := store.NewStore(testdb.New(t))
	svc, err := New(st, Config{Secret: strings.Repeat("s", 32)})
	require.NoError(t, err)
	return svc, st
}

var meta = SessionMeta{IPAddress: "203.0.113.7", UserAgent: "test-agent"}

func TestCreateUserAndLogin(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	user, err := CreateUser(ctx, st, "Nick@example.com", "Nick H", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	require.Equal(t, "Nick@example.com", user.Email)
	require.False(t, user.TwoFactorEnabled)

	// Emails are unique case-insensitively.
	_, err = CreateUser(ctx, st, "nick@example.com", "", "hunter2hunter2", RoleMember)
	require.ErrorIs(t, err, ErrEmailTaken)

	// Login matches case-insensitively too.
	res, err := svc.Login(ctx, "NICK@EXAMPLE.COM", "hunter2hunter2", meta)
	require.NoError(t, err)
	require.NotNil(t, res.Session)
	require.Nil(t, res.Challenge)
	require.Len(t, res.Session.Token, 43)
	require.Equal(t, user.ID, res.Session.User.ID)

	gotUser, gotSess, err := svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)
	require.Equal(t, user.ID, gotUser.ID)
	require.Equal(t, res.Session.ID, gotSess.ID)
	require.Equal(t, meta.IPAddress, gotSess.IpAddress)

	_, err = svc.Login(ctx, "nick@example.com", "wrong-password", meta)
	require.ErrorIs(t, err, ErrInvalidCredentials)

	_, err = svc.Login(ctx, "who-is-this@example.com", "hunter2hunter2", meta)
	require.ErrorIs(t, err, ErrInvalidCredentials)

	_, _, err = svc.Authenticate(ctx, "not-a-real-token")
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestCreateUserValidation(t *testing.T) {
	_, st := newTestService(t)
	ctx := context.Background()

	for _, email := range []string{
		"",
		"not-an-email",
		"missing-domain@",
		"Nick <nick@example.com>", // display-name form must not be normalized away
		" nick@example.com",       // surrounding whitespace
	} {
		_, err := CreateUser(ctx, st, email, "", "hunter2hunter2", RoleMember)
		require.ErrorIs(t, err, ErrInvalidEmail, "email=%q", email)
	}

	_, err := CreateUser(ctx, st, "nick@example.com", "", "short", RoleMember)
	require.ErrorIs(t, err, ErrWeakPassword)
}

func TestLogout(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	_, err := CreateUser(ctx, st, "nick@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)

	require.NoError(t, svc.Logout(ctx, res.Session.Token))
	_, _, err = svc.Authenticate(ctx, res.Session.Token)
	require.ErrorIs(t, err, ErrInvalidToken)
	require.ErrorIs(t, svc.Logout(ctx, res.Session.Token), ErrInvalidToken)
}

func TestSessionExpiryAndSlidingRefresh(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	_, err := CreateUser(ctx, st, "nick@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	originalExpiry := res.Session.ExpiresAt

	// Within update-age: no refresh.
	_, sess, err := svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)
	require.Equal(t, originalExpiry.UTC(), sess.ExpiresAt.UTC())

	// Past the update-age: expiry slides forward.
	svc.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	_, sess, err = svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)
	require.True(t, sess.ExpiresAt.After(originalExpiry))

	// Past the (extended) TTL: invalid.
	svc.now = func() time.Time { return time.Now().Add(25*time.Hour + 31*24*time.Hour) }
	_, _, err = svc.Authenticate(ctx, res.Session.Token)
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestReauthenticatePasswordUser(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	_, err := CreateUser(ctx, st, "nick@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)

	user, sess, err := svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)
	require.True(t, svc.IsSessionFresh(sess), "a session is fresh right after login")

	// Past the reauth window (but well within the session TTL).
	base := time.Now()
	svc.now = func() time.Time { return base.Add(16 * time.Minute) }
	_, sess, err = svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)
	require.False(t, svc.IsSessionFresh(sess))

	// A failed attempt must not refresh the window.
	err = svc.Reauthenticate(ctx, user, sess.ID, "wrong", "", meta.IPAddress)
	require.ErrorIs(t, err, ErrInvalidCredentials)
	_, sess, err = svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)
	require.False(t, svc.IsSessionFresh(sess))

	require.NoError(t, svc.Reauthenticate(ctx, user, sess.ID, "hunter2hunter2", "", meta.IPAddress))
	_, sess, err = svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)
	require.True(t, svc.IsSessionFresh(sess))
}

func TestReauthenticateTwoFactorUser(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	enr, now := enrollTwoFactor(t, svc, st, "nick@example.com")
	user, err := st.GetUserByEmail(ctx, "nick@example.com")
	require.NoError(t, err)

	// Open a session through the challenge flow.
	*now = now.Add(30 * time.Second)
	code, err := totp.GenerateCode(enr.Secret, *now)
	require.NoError(t, err)
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	sess, err := svc.VerifyTwoFactor(ctx, res.Challenge.Token, code, meta)
	require.NoError(t, err)

	*now = now.Add(16 * time.Minute)
	_, dbSess, err := svc.Authenticate(ctx, sess.Token)
	require.NoError(t, err)
	require.False(t, svc.IsSessionFresh(dbSess))

	// 2FA users must present a code; the password does not count.
	err = svc.Reauthenticate(ctx, &user, dbSess.ID, "hunter2hunter2", "", meta.IPAddress)
	require.ErrorIs(t, err, ErrInvalidCode)

	code, err = totp.GenerateCode(enr.Secret, *now)
	require.NoError(t, err)
	require.NoError(t, svc.Reauthenticate(ctx, &user, dbSess.ID, "", code, meta.IPAddress))
	_, dbSess, err = svc.Authenticate(ctx, sess.Token)
	require.NoError(t, err)
	require.True(t, svc.IsSessionFresh(dbSess))

	// The reauth consumed the TOTP step: the same code cannot be replayed.
	*now = now.Add(16 * time.Minute)
	err = svc.Reauthenticate(ctx, &user, dbSess.ID, "", code, meta.IPAddress)
	require.ErrorIs(t, err, ErrInvalidCode)
}

// A pending (unconfirmed) enrollment leaves TwoFactorEnabled false, so reauth
// still runs the password path.
func TestReauthenticatePendingEnrollmentUsesPassword(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	_, err := CreateUser(ctx, st, "nick@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	user, sess, err := svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)

	_, err = svc.EnableTwoFactor(ctx, user.ID)
	require.NoError(t, err)

	require.NoError(t, svc.Reauthenticate(ctx, user, sess.ID, "hunter2hunter2", "", meta.IPAddress))
}

// The sliding session refresh must never count as a reauthentication.
func TestSlidingRefreshDoesNotBumpReauth(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	_, err := CreateUser(ctx, st, "nick@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	_, before, err := svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)

	// Past the update-age: Authenticate slides the expiry…
	base := time.Now()
	svc.now = func() time.Time { return base.Add(25 * time.Hour) }
	_, after, err := svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)
	require.True(t, after.ExpiresAt.After(before.ExpiresAt))

	// …but the reauth stamp is untouched and the session is stale.
	require.Equal(t, before.ReauthenticatedAt.UTC(), after.ReauthenticatedAt.UTC())
	require.False(t, svc.IsSessionFresh(after))
}

func TestReauthenticateRateLimited(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	_, err := CreateUser(ctx, st, "nick@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	user, sess, err := svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)

	for range reauthUserLimit {
		err = svc.Reauthenticate(ctx, user, sess.ID, "wrong", "", meta.IPAddress)
		require.ErrorIs(t, err, ErrInvalidCredentials)
	}
	// Budget exhausted: even the correct password is rejected.
	err = svc.Reauthenticate(ctx, user, sess.ID, "hunter2hunter2", "", meta.IPAddress)
	require.ErrorIs(t, err, ErrRateLimited)

	// A fresh window clears the limiter.
	base := time.Now()
	svc.now = func() time.Time { return base.Add(rateLimitWindow + time.Minute) }
	require.NoError(t, svc.Reauthenticate(ctx, user, sess.ID, "hunter2hunter2", "", meta.IPAddress))
}

func TestReauthenticateSessionGone(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	_, err := CreateUser(ctx, st, "nick@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	user, sess, err := svc.Authenticate(ctx, res.Session.Token)
	require.NoError(t, err)

	require.NoError(t, svc.Logout(ctx, res.Session.Token))
	err = svc.Reauthenticate(ctx, user, sess.ID, "hunter2hunter2", "", meta.IPAddress)
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestListAndRevokeSessions(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	user, err := CreateUser(ctx, st, "nick@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	other, err := CreateUser(ctx, st, "mallory@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)

	s1, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	s2, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)

	sessions, err := svc.ListSessions(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 2)

	// Another user cannot revoke nick's session.
	require.ErrorIs(t, svc.RevokeSession(ctx, other.ID, s2.Session.ID), ErrNotFound)

	require.NoError(t, svc.RevokeSession(ctx, user.ID, s2.Session.ID))
	sessions, err = svc.ListSessions(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, s1.Session.ID, sessions[0].ID)

	_, _, err = svc.Authenticate(ctx, s2.Session.Token)
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestChangePassword(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	user, err := CreateUser(ctx, st, "nick@example.com", "", "old-password", RoleMember)
	require.NoError(t, err)
	keep, err := svc.Login(ctx, "nick@example.com", "old-password", meta)
	require.NoError(t, err)
	otherSess, err := svc.Login(ctx, "nick@example.com", "old-password", meta)
	require.NoError(t, err)

	require.ErrorIs(t, svc.ChangePassword(ctx, user.ID, "wrong", "new-password", keep.Session.ID), ErrInvalidCredentials)
	require.ErrorIs(t, svc.ChangePassword(ctx, user.ID, "old-password", "short", keep.Session.ID), ErrWeakPassword)

	require.NoError(t, svc.ChangePassword(ctx, user.ID, "old-password", "new-password", keep.Session.ID))

	// The requesting session survives; the other one is revoked.
	_, _, err = svc.Authenticate(ctx, keep.Session.Token)
	require.NoError(t, err)
	_, _, err = svc.Authenticate(ctx, otherSess.Session.Token)
	require.ErrorIs(t, err, ErrInvalidToken)

	_, err = svc.Login(ctx, "nick@example.com", "old-password", meta)
	require.ErrorIs(t, err, ErrInvalidCredentials)
	_, err = svc.Login(ctx, "nick@example.com", "new-password", meta)
	require.NoError(t, err)
}

// enrollTwoFactor creates a user, enables and confirms 2FA, and returns the
// service pinned to a controllable clock plus the enrollment.
func enrollTwoFactor(t *testing.T, svc *Service, st *store.Store, email string) (*TwoFactorEnrollment, *time.Time) {
	t.Helper()
	ctx := context.Background()

	user, err := CreateUser(ctx, st, email, "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)

	now := time.Now()
	svc.now = func() time.Time { return now }

	enr, err := svc.EnableTwoFactor(ctx, user.ID)
	require.NoError(t, err)
	require.NotEmpty(t, enr.Secret)
	require.Contains(t, enr.OTPAuthURI, "otpauth://totp/")
	require.Len(t, enr.BackupCodes, backupCodeCount)

	// Pending enrollment: login is still password-only.
	res, err := svc.Login(ctx, email, "hunter2hunter2", meta)
	require.NoError(t, err)
	require.NotNil(t, res.Session)

	code, err := totp.GenerateCode(enr.Secret, now)
	require.NoError(t, err)
	require.NoError(t, svc.ConfirmTwoFactor(ctx, user.ID, code))

	return enr, &now
}

func TestTwoFactorFullFlow(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	enr, now := enrollTwoFactor(t, svc, st, "nick@example.com")

	// Login now yields a challenge, not a session.
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	require.Nil(t, res.Session)
	require.NotNil(t, res.Challenge)

	// The confirmation code's step is burnt; move one step forward.
	*now = now.Add(30 * time.Second)
	code, err := totp.GenerateCode(enr.Secret, *now)
	require.NoError(t, err)

	sess, err := svc.VerifyTwoFactor(ctx, res.Challenge.Token, code, meta)
	require.NoError(t, err)
	_, _, err = svc.Authenticate(ctx, sess.Token)
	require.NoError(t, err)

	// The challenge is single-use.
	_, err = svc.VerifyTwoFactor(ctx, res.Challenge.Token, code, meta)
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestTwoFactorReplayRejected(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	enr, now := enrollTwoFactor(t, svc, st, "nick@example.com")

	*now = now.Add(30 * time.Second)
	code, err := totp.GenerateCode(enr.Secret, *now)
	require.NoError(t, err)

	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	_, err = svc.VerifyTwoFactor(ctx, res.Challenge.Token, code, meta)
	require.NoError(t, err)

	// The same code against a fresh challenge is a replay.
	res2, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	_, err = svc.VerifyTwoFactor(ctx, res2.Challenge.Token, code, meta)
	require.ErrorIs(t, err, ErrInvalidCode)
}

func TestBackupCodeSingleUse(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	enr, _ := enrollTwoFactor(t, svc, st, "nick@example.com")
	backup := enr.BackupCodes[0]

	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	// Backup codes tolerate sloppy input.
	sess, err := svc.VerifyTwoFactor(ctx, res.Challenge.Token, strings.ToUpper(backup), meta)
	require.NoError(t, err)
	require.NotEmpty(t, sess.Token)

	res2, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	_, err = svc.VerifyTwoFactor(ctx, res2.Challenge.Token, backup, meta)
	require.ErrorIs(t, err, ErrInvalidCode)
}

func TestChallengeAttemptCap(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	enr, now := enrollTwoFactor(t, svc, st, "nick@example.com")

	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)

	for i := range challengeMaxAttempts {
		_, err = svc.VerifyTwoFactor(ctx, res.Challenge.Token, "000000", meta)
		require.ErrorIs(t, err, ErrInvalidCode, "attempt %d", i+1)
	}

	// Challenge is deleted after the cap — even a valid code is refused.
	*now = now.Add(30 * time.Second)
	code, err := totp.GenerateCode(enr.Secret, *now)
	require.NoError(t, err)
	_, err = svc.VerifyTwoFactor(ctx, res.Challenge.Token, code, meta)
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestChallengeExpiry(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	enr, now := enrollTwoFactor(t, svc, st, "nick@example.com")

	// Backdate the clock so the challenge is created already expired
	// (its SQL lookup compares against database now()).
	*now = now.Add(-10 * time.Minute)
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)

	*now = now.Add(10 * time.Minute)
	code, err := totp.GenerateCode(enr.Secret, *now)
	require.NoError(t, err)
	_, err = svc.VerifyTwoFactor(ctx, res.Challenge.Token, code, meta)
	require.ErrorIs(t, err, ErrInvalidToken)
}

func TestDisableTwoFactor(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	enrollTwoFactor(t, svc, st, "nick@example.com")
	user, err := st.GetUserByEmail(ctx, "nick@example.com")
	require.NoError(t, err)
	require.True(t, user.TwoFactorEnabled)

	require.NoError(t, svc.DisableTwoFactor(ctx, user.ID))

	// Login is password-only again.
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	require.NotNil(t, res.Session)

	require.ErrorIs(t, svc.DisableTwoFactor(ctx, user.ID), ErrTwoFactorNotEnabled)
}

// Disabling cancels a pending (unconfirmed) enrollment too.
func TestDisablePendingEnrollment(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	user, err := CreateUser(ctx, st, "nick@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	_, err = svc.EnableTwoFactor(ctx, user.ID)
	require.NoError(t, err)

	require.NoError(t, svc.DisableTwoFactor(ctx, user.ID))
}

func TestEnableTwoFactorAlreadyEnabled(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	enrollTwoFactor(t, svc, st, "nick@example.com")
	user, err := st.GetUserByEmail(ctx, "nick@example.com")
	require.NoError(t, err)

	_, err = svc.EnableTwoFactor(ctx, user.ID)
	require.ErrorIs(t, err, ErrTwoFactorAlreadyEnabled)
}

func TestRegenerateBackupCodes(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	enr, _ := enrollTwoFactor(t, svc, st, "nick@example.com")
	user, err := st.GetUserByEmail(ctx, "nick@example.com")
	require.NoError(t, err)

	fresh, err := svc.RegenerateBackupCodes(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, fresh, backupCodeCount)
	require.NotEqual(t, enr.BackupCodes, fresh)

	// Old codes are dead, new ones work.
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	_, err = svc.VerifyTwoFactor(ctx, res.Challenge.Token, enr.BackupCodes[0], meta)
	require.ErrorIs(t, err, ErrInvalidCode)

	res, err = svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	_, err = svc.VerifyTwoFactor(ctx, res.Challenge.Token, fresh[0], meta)
	require.NoError(t, err)
}

func TestLoginRateLimited(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	_, err := CreateUser(ctx, st, "nick@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)

	now := time.Now()
	svc.now = func() time.Time { return now }

	for i := range loginUserLimit {
		_, err := svc.Login(ctx, "nick@example.com", "wrong-password", meta)
		require.ErrorIs(t, err, ErrInvalidCredentials, "attempt %d", i+1)
	}
	// Even the right password is refused inside the window.
	_, err = svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.ErrorIs(t, err, ErrRateLimited)

	// A new window clears it.
	now = now.Add(rateLimitWindow)
	_, err = svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
}

func TestSweepExpired(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	// nick (2FA) yields an expired challenge, alice an expired session; the
	// session from nick's pending-phase login stays valid and must survive.
	_, err := CreateUser(ctx, st, "alice@example.com", "", "hunter2hunter2", RoleMember)
	require.NoError(t, err)
	enrollTwoFactor(t, svc, st, "nick@example.com")

	// Backdate the clock: everything created now is born expired.
	past := time.Now().Add(-31 * 24 * time.Hour)
	svc.now = func() time.Time { return past }
	_, err = svc.Login(ctx, "alice@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	res, err := svc.Login(ctx, "nick@example.com", "hunter2hunter2", meta)
	require.NoError(t, err)
	require.NotNil(t, res.Challenge)

	count := func(table string) (n int) {
		require.NoError(t, st.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n))
		return n
	}
	require.Equal(t, 2, count("sessions"))
	require.Equal(t, 1, count("login_challenges"))

	require.NoError(t, svc.SweepExpired(ctx))

	require.Equal(t, 1, count("sessions"))
	require.Equal(t, 0, count("login_challenges"))
}
