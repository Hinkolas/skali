// Package auth implements app-user authentication: email+password login
// with argon2id hashing, opaque DB-backed bearer sessions, and TOTP two-factor
// with single-use backup codes. It is deliberately self-contained (no external
// IdP) per the self-hosting constraint; the Authenticator interface is the
// seam a future OIDC integration would slot into.
//
// There is no registration or password-recovery flow by design: users are
// created by the operator (see CreateUser, wired to the skalid binary's CLI).
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Hinkolas/skali/internal/store"
)

// Config carries the auth policy. Zero values fall back to the defaults below;
// only Secret is required.
type Config struct {
	// Secret is the operator's AUTH_SECRET; it keys at-rest encryption of
	// TOTP secrets. Rotating it forces users to re-enroll 2FA.
	Secret           string
	SessionTTL       time.Duration // bearer session lifetime
	SessionUpdateAge time.Duration // sliding refresh: extend when a session's last touch is older than this
	ChallengeTTL     time.Duration // window between password login and 2FA code entry
}

const (
	defaultSessionTTL       = 30 * 24 * time.Hour
	defaultSessionUpdateAge = 24 * time.Hour
	defaultChallengeTTL     = 5 * time.Minute
)

// Login attempt limits (fixed windows, per process).
const (
	loginUserLimit  = 10
	loginIPLimit    = 30
	twoFactorLimit  = 30
	rateLimitWindow = 15 * time.Minute
	// challengeMaxAttempts caps code guesses per login challenge; afterwards
	// the user must present the password again.
	challengeMaxAttempts = 5
)

// Service is the concrete Authenticator over the Postgres store.
type Service struct {
	st      *store.Store
	cfg     Config
	key     []byte // AES-256 key derived from cfg.Secret
	limiter *rateLimiter
	now     func() time.Time // test seam; limiter resolves it dynamically
}

// Authenticator validates bearer tokens; the HTTP middleware depends on this
// interface rather than on *Service.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*store.User, *store.Session, error)
}

func New(st *store.Store, cfg Config) (*Service, error) {
	if len(cfg.Secret) < 32 {
		return nil, fmt.Errorf("auth: secret must be at least 32 characters")
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = defaultSessionTTL
	}
	if cfg.SessionUpdateAge == 0 {
		cfg.SessionUpdateAge = defaultSessionUpdateAge
	}
	if cfg.ChallengeTTL == 0 {
		cfg.ChallengeTTL = defaultChallengeTTL
	}
	key, err := deriveKey(cfg.Secret)
	if err != nil {
		return nil, fmt.Errorf("auth: derive key: %w", err)
	}
	s := &Service{st: st, cfg: cfg, key: key, now: time.Now}
	s.limiter = newRateLimiter(func() time.Time { return s.now() })
	return s, nil
}

// SessionMeta is client context recorded on new sessions.
type SessionMeta struct {
	IPAddress string
	UserAgent string
}

// Session is a freshly created login session. Token is the plaintext bearer
// token — this is the only place it ever exists.
type Session struct {
	ID        uuid.UUID
	Token     string
	ExpiresAt time.Time
	User      store.User
}

// Challenge is the intermediate step of a 2FA login.
type Challenge struct {
	Token     string
	ExpiresAt time.Time
}

// LoginResult has exactly one field set: Session when the user has no 2FA,
// Challenge when a second factor is required.
type LoginResult struct {
	Session   *Session
	Challenge *Challenge
}

// TwoFactorEnrollment is returned once from EnableTwoFactor; none of it is
// recoverable afterwards.
type TwoFactorEnrollment struct {
	Secret      string
	OTPAuthURI  string
	BackupCodes []string
}

// Login verifies email+password and either opens a session or, when 2FA is
// enabled, hands back a short-lived challenge for VerifyTwoFactor.
func (s *Service) Login(ctx context.Context, email, password string, meta SessionMeta) (*LoginResult, error) {
	userOK := s.limiter.allow("login:user:"+strings.ToLower(email), loginUserLimit, rateLimitWindow)
	ipOK := s.limiter.allow("login:ip:"+meta.IPAddress, loginIPLimit, rateLimitWindow)
	if !userOK || !ipOK {
		return nil, ErrRateLimited
	}

	user, err := s.st.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Pay the argon2 cost anyway so response timing does not reveal
			// whether the email exists.
			verifyPassword(password, dummyHash())
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if err := s.verifyUserPassword(ctx, user.ID, password); err != nil {
		return nil, err
	}

	if user.TwoFactorEnabled {
		ch, err := s.createChallenge(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		return &LoginResult{Challenge: ch}, nil
	}
	sess, err := s.createSession(ctx, s.st.Queries, user, meta)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Session: sess}, nil
}

// VerifyTwoFactor exchanges a login challenge plus a TOTP or backup code for a
// session.
func (s *Service) VerifyTwoFactor(ctx context.Context, challengeToken, code string, meta SessionMeta) (*Session, error) {
	if !s.limiter.allow("2fa:ip:"+meta.IPAddress, twoFactorLimit, rateLimitWindow) {
		return nil, ErrRateLimited
	}

	ch, err := s.st.GetLoginChallengeByTokenHash(ctx, hashToken(challengeToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}

	ok, err := s.checkSecondFactor(ctx, ch.UserID, code)
	if err != nil {
		return nil, err
	}
	if !ok {
		attempts, err := s.st.IncrementLoginChallengeAttempts(ctx, ch.ID)
		if err == nil && attempts >= challengeMaxAttempts {
			_ = s.st.DeleteLoginChallenge(ctx, ch.ID)
		}
		return nil, ErrInvalidCode
	}

	user, err := s.st.GetUserByID(ctx, ch.UserID)
	if err != nil {
		return nil, err
	}
	var sess *Session
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		if err := q.DeleteLoginChallenge(ctx, ch.ID); err != nil {
			return err
		}
		sess, err = s.createSession(ctx, q, user, meta)
		return err
	})
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// Authenticate resolves a bearer token to its user and session, applying the
// sliding expiration policy. This is the request hot path.
func (s *Service) Authenticate(ctx context.Context, token string) (*store.User, *store.Session, error) {
	row, err := s.st.GetSessionAndUserByTokenHash(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrInvalidToken
		}
		return nil, nil, err
	}
	now := s.now()
	if !row.Session.ExpiresAt.After(now) {
		return nil, nil, ErrInvalidToken
	}
	if now.Sub(row.Session.UpdatedAt) > s.cfg.SessionUpdateAge {
		exp := now.Add(s.cfg.SessionTTL)
		if err := s.st.ExtendSession(ctx, store.ExtendSessionParams{ID: row.Session.ID, ExpiresAt: exp}); err != nil {
			// Best effort: the session is still valid, only the refresh failed.
			slog.WarnContext(ctx, "auth: extend session", "err", err)
		} else {
			row.Session.ExpiresAt = exp
		}
	}
	return &row.User, &row.Session, nil
}

// Logout revokes the session behind the given bearer token.
func (s *Service) Logout(ctx context.Context, token string) error {
	n, err := s.st.DeleteSessionByTokenHash(ctx, hashToken(token))
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInvalidToken
	}
	return nil
}

func (s *Service) ListSessions(ctx context.Context, userID uuid.UUID) ([]store.Session, error) {
	return s.st.ListSessionsByUser(ctx, userID)
}

// RevokeSession deletes one of the user's own sessions.
func (s *Service) RevokeSession(ctx context.Context, userID, sessionID uuid.UUID) error {
	n, err := s.st.DeleteSessionByID(ctx, store.DeleteSessionByIDParams{ID: sessionID, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ChangePassword sets a new password and revokes every other session, keeping
// only the one the request came from.
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, current, newPassword string, keepSession uuid.UUID) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	if err := s.verifyUserPassword(ctx, userID, current); err != nil {
		return err
	}
	phc, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		if err := q.UpdateAccountPassword(ctx, store.UpdateAccountPasswordParams{UserID: userID, Password: &phc}); err != nil {
			return err
		}
		_, err := q.DeleteUserSessionsExcept(ctx, store.DeleteUserSessionsExceptParams{UserID: userID, ID: keepSession})
		return err
	})
}

// EnableTwoFactor starts (or restarts) TOTP enrollment. The enrollment stays
// pending — and login stays password-only — until ConfirmTwoFactor sees a
// valid code.
func (s *Service) EnableTwoFactor(ctx context.Context, userID uuid.UUID, password string) (*TwoFactorEnrollment, error) {
	if err := s.verifyUserPassword(ctx, userID, password); err != nil {
		return nil, err
	}
	tf, err := s.st.GetTwoFactorByUserID(ctx, userID)
	if err == nil && tf.ConfirmedAt != nil {
		return nil, ErrTwoFactorAlreadyEnabled
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	user, err := s.st.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	key, err := generateTOTP(user.Email)
	if err != nil {
		return nil, err
	}
	encSecret, err := encrypt(s.key, []byte(key.Secret()))
	if err != nil {
		return nil, err
	}
	codes, err := generateBackupCodes()
	if err != nil {
		return nil, err
	}

	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		row, err := q.UpsertPendingTwoFactor(ctx, store.UpsertPendingTwoFactorParams{ID: id, UserID: userID, Secret: encSecret})
		if err != nil {
			return err
		}
		if err := q.DeleteBackupCodesByTwoFactorID(ctx, row.ID); err != nil {
			return err
		}
		return s.insertBackupCodes(ctx, q, row.ID, codes)
	})
	if err != nil {
		return nil, err
	}
	return &TwoFactorEnrollment{Secret: key.Secret(), OTPAuthURI: key.URL(), BackupCodes: codes}, nil
}

// ConfirmTwoFactor activates a pending enrollment once the user proves their
// authenticator produces valid codes.
func (s *Service) ConfirmTwoFactor(ctx context.Context, userID uuid.UUID, code string) error {
	tf, err := s.st.GetTwoFactorByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTwoFactorNotEnabled
		}
		return err
	}
	if tf.ConfirmedAt != nil {
		return ErrTwoFactorAlreadyEnabled
	}
	secret, err := decrypt(s.key, tf.Secret)
	if err != nil {
		return err
	}
	step, ok := matchTOTPStep(string(secret), code, s.now())
	if !ok {
		return ErrInvalidCode
	}
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		n, err := q.ConfirmTwoFactor(ctx, userID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrTwoFactorNotEnabled
		}
		// Burn the confirming code so it cannot be replayed at login.
		if _, err := q.AdvanceTwoFactorStep(ctx, store.AdvanceTwoFactorStepParams{UserID: userID, LastUsedStep: step}); err != nil {
			return err
		}
		return q.SetUserTwoFactorEnabled(ctx, store.SetUserTwoFactorEnabledParams{ID: userID, TwoFactorEnabled: true})
	})
}

// DisableTwoFactor removes 2FA. A confirmed enrollment additionally requires a
// valid TOTP or backup code; a pending one is simply cancelled.
func (s *Service) DisableTwoFactor(ctx context.Context, userID uuid.UUID, password, code string) error {
	if err := s.verifyUserPassword(ctx, userID, password); err != nil {
		return err
	}
	tf, err := s.st.GetTwoFactorByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTwoFactorNotEnabled
		}
		return err
	}
	if tf.ConfirmedAt != nil {
		ok, err := s.checkSecondFactor(ctx, userID, code)
		if err != nil {
			return err
		}
		if !ok {
			return ErrInvalidCode
		}
	}
	return s.st.WithTx(ctx, func(q *store.Queries) error {
		if _, err := q.DeleteTwoFactorByUserID(ctx, userID); err != nil {
			return err
		}
		return q.SetUserTwoFactorEnabled(ctx, store.SetUserTwoFactorEnabledParams{ID: userID, TwoFactorEnabled: false})
	})
}

// RegenerateBackupCodes replaces all backup codes (used and unused).
func (s *Service) RegenerateBackupCodes(ctx context.Context, userID uuid.UUID, password string) ([]string, error) {
	if err := s.verifyUserPassword(ctx, userID, password); err != nil {
		return nil, err
	}
	tf, err := s.st.GetTwoFactorByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTwoFactorNotEnabled
		}
		return nil, err
	}
	if tf.ConfirmedAt == nil {
		return nil, ErrTwoFactorNotEnabled
	}
	codes, err := generateBackupCodes()
	if err != nil {
		return nil, err
	}
	err = s.st.WithTx(ctx, func(q *store.Queries) error {
		if err := q.DeleteBackupCodesByTwoFactorID(ctx, tf.ID); err != nil {
			return err
		}
		return s.insertBackupCodes(ctx, q, tf.ID, codes)
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// SweepExpired removes expired sessions and login challenges; the skalid
// binary runs it periodically.
func (s *Service) SweepExpired(ctx context.Context) error {
	if _, err := s.st.DeleteExpiredSessions(ctx); err != nil {
		return err
	}
	_, err := s.st.DeleteExpiredLoginChallenges(ctx)
	return err
}

// ErrInvalidEmail/ErrWeakPassword live here rather than errors.go because
// their rules do too.
var (
	ErrInvalidEmail = errors.New("auth: invalid email address")
	ErrWeakPassword = errors.New("auth: password must be at least 8 characters")
)

// validateEmail accepts a bare RFC 5322 address (no display name). The parsed
// address must round-trip to the input so forms like "Name <a@b>" or trailing
// whitespace are rejected rather than silently normalized.
func validateEmail(email string) error {
	if len(email) > 254 {
		return ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return ErrInvalidEmail
	}
	return nil
}

func validatePassword(password string) error {
	if len(password) < 8 {
		return ErrWeakPassword
	}
	return nil
}

// CreateUser provisions a user with a credential account. It is package-level
// (not a Service method) so the operator CLI can create users with only a
// database connection — no AUTH_SECRET required.
func CreateUser(ctx context.Context, st *store.Store, email, name, password string) (*store.User, error) {
	if err := validateEmail(email); err != nil {
		return nil, err
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	phc, err := hashPassword(password)
	if err != nil {
		return nil, err
	}

	var user store.User
	err = st.WithTx(ctx, func(q *store.Queries) error {
		userID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		user, err = q.CreateUser(ctx, store.CreateUserParams{ID: userID, Email: email, Name: name})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
				return ErrEmailTaken
			}
			return err
		}
		acctID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		_, err = q.CreateAccount(ctx, store.CreateAccountParams{ID: acctID, UserID: user.ID, ProviderID: "credential", Password: &phc})
		return err
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// verifyUserPassword checks password against the user's credential account.
func (s *Service) verifyUserPassword(ctx context.Context, userID uuid.UUID, password string) error {
	acct, err := s.st.GetCredentialAccount(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			verifyPassword(password, dummyHash())
			return ErrInvalidCredentials
		}
		return err
	}
	if acct.Password == nil || !verifyPassword(password, *acct.Password) {
		return ErrInvalidCredentials
	}
	return nil
}

// checkSecondFactor validates a TOTP or backup code for a user with confirmed
// 2FA, consuming it (step advance / code burn) on success.
func (s *Service) checkSecondFactor(ctx context.Context, userID uuid.UUID, code string) (bool, error) {
	tf, err := s.st.GetTwoFactorByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if tf.ConfirmedAt == nil {
		return false, nil
	}

	if isTOTPCode(code) {
		secret, err := decrypt(s.key, tf.Secret)
		if err != nil {
			return false, err
		}
		step, ok := matchTOTPStep(string(secret), code, s.now())
		if !ok {
			return false, nil
		}
		// The step only ever advances; 0 rows here means the code was already
		// used — a replay.
		n, err := s.st.AdvanceTwoFactorStep(ctx, store.AdvanceTwoFactorStepParams{UserID: userID, LastUsedStep: step})
		if err != nil {
			return false, err
		}
		return n == 1, nil
	}

	// Backup code path.
	norm := normalizeBackupCode(code)
	unused, err := s.st.ListUnusedBackupCodes(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, bc := range unused {
		if verifyPassword(norm, bc.CodeHash) {
			n, err := s.st.ConsumeBackupCode(ctx, bc.ID)
			if err != nil {
				return false, err
			}
			return n == 1, nil
		}
	}
	return false, nil
}

func isTOTPCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (s *Service) createChallenge(ctx context.Context, userID uuid.UUID) (*Challenge, error) {
	tok, err := newToken()
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	row, err := s.st.CreateLoginChallenge(ctx, store.CreateLoginChallengeParams{
		ID:        id,
		UserID:    userID,
		TokenHash: hashToken(tok),
		ExpiresAt: s.now().Add(s.cfg.ChallengeTTL),
	})
	if err != nil {
		return nil, err
	}
	return &Challenge{Token: tok, ExpiresAt: row.ExpiresAt}, nil
}

func (s *Service) createSession(ctx context.Context, q *store.Queries, user store.User, meta SessionMeta) (*Session, error) {
	tok, err := newToken()
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	row, err := q.CreateSession(ctx, store.CreateSessionParams{
		ID:        id,
		UserID:    user.ID,
		TokenHash: hashToken(tok),
		ExpiresAt: s.now().Add(s.cfg.SessionTTL),
		IpAddress: meta.IPAddress,
		UserAgent: meta.UserAgent,
	})
	if err != nil {
		return nil, err
	}
	return &Session{ID: row.ID, Token: tok, ExpiresAt: row.ExpiresAt, User: user}, nil
}

// insertBackupCodes hashes and stores display-form codes.
func (s *Service) insertBackupCodes(ctx context.Context, q *store.Queries, twoFactorID uuid.UUID, codes []string) error {
	for _, code := range codes {
		hash, err := hashPassword(normalizeBackupCode(code))
		if err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if err := q.CreateBackupCode(ctx, store.CreateBackupCodeParams{ID: id, TwoFactorID: twoFactorID, CodeHash: hash}); err != nil {
			return err
		}
	}
	return nil
}
