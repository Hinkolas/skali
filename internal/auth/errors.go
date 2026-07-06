package auth

import "errors"

// Sentinel errors returned by the Service. The HTTP layer maps these onto the
// error envelope; anything not listed here is treated as an internal error.
var (
	ErrInvalidCredentials      = errors.New("auth: invalid email or password")
	ErrInvalidToken            = errors.New("auth: invalid or expired token")
	ErrInvalidCode             = errors.New("auth: invalid code")
	ErrRateLimited             = errors.New("auth: too many attempts")
	ErrTwoFactorAlreadyEnabled = errors.New("auth: two-factor already enabled")
	ErrTwoFactorNotEnabled     = errors.New("auth: two-factor not enabled")
	ErrNotFound                = errors.New("auth: not found")
	ErrEmailTaken              = errors.New("auth: email already taken")
	ErrInvalidRole             = errors.New("auth: role must be admin or member")
	ErrLastAdmin               = errors.New("auth: cannot demote or delete the last admin")
	ErrReauthRequired          = errors.New("auth: recent authentication required")
)
