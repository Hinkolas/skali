package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Hinkolas/skali/internal/auth"
)

// errorBody is the error envelope every non-2xx response uses:
// {"error":{"code":"…","message":"…"}}. The codes are enumerated in the
// OpenAPI spec; clients should branch on code, not message.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	codeBadRequest         = "bad_request"
	codeInvalidCredentials = "invalid_credentials"
	codeInvalidToken       = "invalid_token"
	codeInvalidCode        = "invalid_code"
	codeNotFound           = "not_found"
	codeConflict           = "conflict"
	codeRateLimited        = "rate_limited"
	codeInternal           = "internal"
)

// writeAuthError maps auth sentinel errors onto the envelope; anything
// unrecognized is logged and reported as an opaque 500.
func writeAuthError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, codeInvalidCredentials, "invalid email or password")
	case errors.Is(err, auth.ErrInvalidToken):
		writeError(w, http.StatusUnauthorized, codeInvalidToken, "invalid or expired token")
	case errors.Is(err, auth.ErrInvalidCode):
		writeError(w, http.StatusUnauthorized, codeInvalidCode, "invalid code")
	case errors.Is(err, auth.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many attempts, try again later")
	case errors.Is(err, auth.ErrTwoFactorAlreadyEnabled):
		writeError(w, http.StatusConflict, codeConflict, "two-factor authentication is already enabled")
	case errors.Is(err, auth.ErrTwoFactorNotEnabled):
		writeError(w, http.StatusConflict, codeConflict, "two-factor authentication is not enabled")
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrInvalidEmail):
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
	default:
		slog.ErrorContext(ctx, "api: internal error", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}
