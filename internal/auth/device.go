package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/store"
)

// Browser device authorization: the CLI opens a request, shows the user
// code, and polls with the device code while the signed-in browser looks
// the user code up and approves it. Two intents share the table. A login
// request mints a session for whoever approves it, handed to the CLI on
// its next poll. A reauth request is bound to the CLI's own session at
// creation; approval by that session's owner re-stamps it, so a
// sudo-gated command confirms identity in the browser (where a password
// manager fills the checkpoint) instead of retyping the password.

// DeviceIntent names what an approval grants.
type DeviceIntent string

const (
	DeviceLogin  DeviceIntent = "login"
	DeviceReauth DeviceIntent = "reauth"
)

// Stored request status.
const (
	deviceStatusPending = "pending"
	deviceStatusDenied  = "denied"
)

// Device request status as reported to the polling CLI.
const (
	DevicePending  = "pending"
	DeviceApproved = "approved"
	DeviceDenied   = "denied"
	DeviceExpired  = "expired"
)

// Device flow tuning.
const (
	defaultDeviceTTL = 10 * time.Minute
	// DevicePollInterval is the cadence the CLI is told to poll at; a poll
	// arriving well before it answers slow_down.
	DevicePollInterval = 5 * time.Second
	devicePollSlack    = 1 * time.Second
	// devicePollCap treats a request that was polled far more often than
	// the interval allows within the TTL as abandoned.
	devicePollCap = 300
	// userCodeLength and userCodeAlphabet: 8 symbols from 28 (no vowels, no
	// 0/O or 1/I) give 38 bits, shown to the user as XXXX-XXXX.
	userCodeLength   = 8
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ23456789"
	deviceLabelMax   = 128

	deviceCreateIPLimit    = 20
	devicePollIPLimit      = 600
	deviceLookupUserLimit  = 30
	deviceApproveUserLimit = 10
)

// DeviceRequest is what the CLI holds while it waits: DeviceCode is the
// polling secret (never stored in plaintext), UserCode is what the person
// confirms in the browser.
type DeviceRequest struct {
	DeviceCode string
	UserCode   string
	ExpiresAt  time.Time
	Interval   time.Duration
}

// DevicePoll is one poll's answer. Session is set exactly once, on the poll
// that observes an approved login request; the request is deleted in the
// same transaction.
type DevicePoll struct {
	Status  string
	Session *Session
}

// DeviceInfo is what the browser sees before approving. Mine is only
// meaningful for reauth: whether the bound session belongs to the caller.
type DeviceInfo struct {
	Intent      DeviceIntent
	ClientLabel string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	Mine        bool
}

// StartDeviceLogin opens an anonymous login request. The caller's address
// and user agent are recorded on the request and become the minted
// session's, so the session list shows the terminal, not the browser.
func (s *Service) StartDeviceLogin(ctx context.Context, label string, meta SessionMeta) (*DeviceRequest, error) {
	if !s.limiter.allow("device:create:ip:"+meta.IPAddress, deviceCreateIPLimit, rateLimitWindow) {
		return nil, ErrRateLimited
	}
	return s.createDeviceRequest(ctx, DeviceLogin, nil, nil, label, meta)
}

// StartDeviceReauth opens a request bound to the caller's current session;
// only that session's owner can approve it, and approval re-stamps that
// session alone.
func (s *Service) StartDeviceReauth(ctx context.Context, user *store.User, sessionID uuid.UUID, label string, meta SessionMeta) (*DeviceRequest, error) {
	if !s.limiter.allow("device:create:ip:"+meta.IPAddress, deviceCreateIPLimit, rateLimitWindow) {
		return nil, ErrRateLimited
	}
	return s.createDeviceRequest(ctx, DeviceReauth, &sessionID, &user.ID, label, meta)
}

func (s *Service) createDeviceRequest(ctx context.Context, intent DeviceIntent, sessionID, userID *uuid.UUID, label string, meta SessionMeta) (*DeviceRequest, error) {
	deviceCode, err := newToken()
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	label = sanitizeDeviceLabel(label)
	expiresAt := s.now().Add(s.cfg.DeviceTTL)
	// The user code is short, so a collision with a live request is
	// possible in principle; retry on the unique violation.
	for attempt := 0; attempt < 5; attempt++ {
		userCode, err := newUserCode()
		if err != nil {
			return nil, err
		}
		row, err := s.st.CreateDeviceRequest(ctx, store.CreateDeviceRequestParams{
			ID:             id,
			Intent:         string(intent),
			UserCode:       userCode,
			DeviceCodeHash: hashToken(deviceCode),
			SessionID:      sessionID,
			UserID:         userID,
			ClientLabel:    label,
			IpAddress:      meta.IPAddress,
			UserAgent:      meta.UserAgent,
			ExpiresAt:      expiresAt,
		})
		if store.IsUniqueViolation(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &DeviceRequest{
			DeviceCode: deviceCode,
			UserCode:   FormatUserCode(row.UserCode),
			ExpiresAt:  row.ExpiresAt,
			Interval:   DevicePollInterval,
		}, nil
	}
	return nil, errors.New("auth: could not allocate a device user code")
}

// PollDevice answers the CLI. Terminal states delete the request, so an
// approved login hands out its token exactly once and an unknown code is
// indistinguishable from an expired one. Polling faster than the interval
// answers ErrSlowDown without touching the request's state.
func (s *Service) PollDevice(ctx context.Context, deviceCode, ip string) (*DevicePoll, error) {
	if !s.limiter.allow("device:poll:ip:"+ip, devicePollIPLimit, rateLimitWindow) {
		return nil, ErrRateLimited
	}
	var result *DevicePoll
	err := s.st.WithTx(ctx, func(q *store.Queries) error {
		row, err := q.GetDeviceRequestByDeviceCodeHashForUpdate(ctx, hashToken(deviceCode))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeviceNotFound
		}
		if err != nil {
			return err
		}
		now := s.now()
		if !row.ExpiresAt.After(now) || row.PollCount >= devicePollCap {
			if err := q.DeleteDeviceRequest(ctx, row.ID); err != nil {
				return err
			}
			result = &DevicePoll{Status: DeviceExpired}
			return nil
		}
		if _, err := q.TouchDeviceRequestPoll(ctx, store.TouchDeviceRequestPollParams{ID: row.ID, LastPolledAt: &now}); err != nil {
			return err
		}
		if row.LastPolledAt != nil && now.Sub(*row.LastPolledAt) < DevicePollInterval-devicePollSlack {
			return ErrSlowDown
		}
		switch row.Status {
		case deviceStatusPending:
			result = &DevicePoll{Status: DevicePending}
			return nil
		case deviceStatusDenied:
			if err := q.DeleteDeviceRequest(ctx, row.ID); err != nil {
				return err
			}
			result = &DevicePoll{Status: DeviceDenied}
			return nil
		}
		// Approved.
		if err := q.DeleteDeviceRequest(ctx, row.ID); err != nil {
			return err
		}
		result = &DevicePoll{Status: DeviceApproved}
		if DeviceIntent(row.Intent) != DeviceLogin {
			return nil
		}
		if row.UserID == nil {
			return errors.New("auth: approved device login without an approver")
		}
		user, err := q.GetUserByID(ctx, *row.UserID)
		if err != nil {
			return err
		}
		sess, err := s.createSession(ctx, q, user, SessionMeta{IPAddress: row.IpAddress, UserAgent: row.UserAgent})
		if err != nil {
			return err
		}
		result.Session = sess
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// LookupDevice describes a pending request to the signed-in browser.
func (s *Service) LookupDevice(ctx context.Context, caller *store.User, userCode string) (*DeviceInfo, error) {
	if !s.limiter.allow("device:lookup:user:"+caller.ID.String(), deviceLookupUserLimit, rateLimitWindow) {
		return nil, ErrRateLimited
	}
	row, err := s.pendingDevice(ctx, userCode)
	if err != nil {
		return nil, err
	}
	return &DeviceInfo{
		Intent:      DeviceIntent(row.Intent),
		ClientLabel: row.ClientLabel,
		CreatedAt:   row.CreatedAt,
		ExpiresAt:   row.ExpiresAt,
		Mine:        row.UserID != nil && *row.UserID == caller.ID,
	}, nil
}

// ApproveDevice records the caller as the approver of a login request, or
// re-stamps the session bound to a reauth request when the caller owns it.
// The HTTP route sits behind the sudo gate, so the caller's own session is
// fresh when this runs.
func (s *Service) ApproveDevice(ctx context.Context, caller *store.User, userCode string) error {
	if !s.limiter.allow("device:approve:user:"+caller.ID.String(), deviceApproveUserLimit, rateLimitWindow) {
		return ErrRateLimited
	}
	row, err := s.pendingDevice(ctx, userCode)
	if err != nil {
		return err
	}
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		if DeviceIntent(row.Intent) == DeviceReauth {
			if row.UserID == nil || *row.UserID != caller.ID || row.SessionID == nil {
				return ErrDeviceForeign
			}
			n, err := q.TouchSessionReauthenticated(ctx, store.TouchSessionReauthenticatedParams{
				ID:                *row.SessionID,
				UserID:            caller.ID,
				ReauthenticatedAt: s.now(),
			})
			if err != nil {
				return err
			}
			if n == 0 {
				// The CLI session was revoked while the browser was open.
				return ErrDeviceNotFound
			}
		}
		n, err := q.ApproveDeviceRequest(ctx, store.ApproveDeviceRequestParams{ID: row.ID, UserID: &caller.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrDeviceNotFound
		}
		return nil
	})
}

// DenyDevice refuses a pending request; the CLI learns on its next poll.
func (s *Service) DenyDevice(ctx context.Context, caller *store.User, userCode string) error {
	row, err := s.pendingDevice(ctx, userCode)
	if err != nil {
		return err
	}
	n, err := s.st.DenyDeviceRequest(ctx, row.ID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrDeviceNotFound
	}
	return nil
}

func (s *Service) pendingDevice(ctx context.Context, userCode string) (store.DeviceRequest, error) {
	normalized, ok := NormalizeUserCode(userCode)
	if !ok {
		return store.DeviceRequest{}, ErrDeviceNotFound
	}
	row, err := s.st.GetDeviceRequestByUserCode(ctx, normalized)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.DeviceRequest{}, ErrDeviceNotFound
	}
	if err != nil {
		return store.DeviceRequest{}, err
	}
	if row.Status != deviceStatusPending || !row.ExpiresAt.After(s.now()) {
		return store.DeviceRequest{}, ErrDeviceNotFound
	}
	return row, nil
}

func newUserCode() (string, error) {
	var code strings.Builder
	buf := make([]byte, 1)
	for code.Len() < userCodeLength {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("auth: user code: %w", err)
		}
		// Rejection sampling keeps the alphabet uniform.
		if int(buf[0]) >= 256-256%len(userCodeAlphabet) {
			continue
		}
		code.WriteByte(userCodeAlphabet[int(buf[0])%len(userCodeAlphabet)])
	}
	return code.String(), nil
}

// FormatUserCode renders the stored form for people: XXXX-XXXX.
func FormatUserCode(code string) string {
	if len(code) != userCodeLength {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// NormalizeUserCode maps what a person typed (any case, with or without
// the dash or spaces) to the stored form, refusing anything that cannot
// be a code so the lookup never hits the database for junk.
func NormalizeUserCode(input string) (string, bool) {
	var code strings.Builder
	for _, r := range strings.ToUpper(input) {
		if r == '-' || r == ' ' {
			continue
		}
		if !strings.ContainsRune(userCodeAlphabet, r) {
			return "", false
		}
		code.WriteRune(r)
	}
	if code.Len() != userCodeLength {
		return "", false
	}
	return code.String(), true
}

// sanitizeDeviceLabel keeps the client's self-description printable and
// short: it is shown to the approver verbatim.
func sanitizeDeviceLabel(label string) string {
	var out strings.Builder
	for _, r := range strings.TrimSpace(label) {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			continue
		}
		out.WriteRune(r)
		if out.Len() >= deviceLabelMax {
			break
		}
	}
	if out.Len() == 0 {
		return "skali CLI"
	}
	return out.String()
}
