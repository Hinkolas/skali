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
	codeBadRequest          = "bad_request"
	codeInvalidCredentials  = "invalid_credentials"
	codeInvalidToken        = "invalid_token"
	codeInvalidCode         = "invalid_code"
	codeForbidden           = "forbidden"
	codeReauthRequired      = "reauth_required"
	codeNotFound            = "not_found"
	codeConflict            = "conflict"
	codeVersionConflict     = "version_conflict"
	codeInvalidManifest     = "invalid_manifest"
	codeRateLimited         = "rate_limited"
	codeNodeUnreachable     = "node_unreachable"
	codeRegistryDisabled    = "registry_disabled"
	codeRegistryUnavailable = "registry_unavailable"
	codeInternal            = "internal"

	// Deployment coordination.
	codeDeploymentInFlight      = "deployment_in_flight"
	codeDestructiveChange       = "destructive_change"
	codeDigestMismatch          = "digest_mismatch"
	codeArtifactsIncomplete     = "artifacts_incomplete"
	codeUnsupportedCapabilities = "unsupported_capabilities"
	codePlatformMismatch        = "platform_mismatch"
	codeInvalidValues           = "invalid_values"
	codeUnsupportedSchema       = "unsupported_schema"
	codeEnvironmentProtected    = "environment_protected"

	// Backups.
	codeBackupInFlight           = "backup_in_flight"
	codeBackupTargetUnconfigured = "backup_target_unconfigured"
	codeBackupTargetUnreachable  = "backup_target_unreachable"
	codeEnvironmentNotActive     = "environment_not_active"
	codeSnapshotNotFound         = "snapshot_not_found"

	// Exec.
	codeNoReadyPod = "no_ready_pod"
)

// writeInternalError logs the real error and reports an opaque 500.
func writeInternalError(ctx context.Context, w http.ResponseWriter, what string, err error) {
	slog.ErrorContext(ctx, "api: "+what, "err", err)
	writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
}

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
	case errors.Is(err, auth.ErrReauthRequired):
		writeError(w, http.StatusForbidden, codeReauthRequired, "recent authentication required")
	case errors.Is(err, auth.ErrTwoFactorAlreadyEnabled):
		writeError(w, http.StatusConflict, codeConflict, "two-factor authentication is already enabled")
	case errors.Is(err, auth.ErrTwoFactorNotEnabled):
		writeError(w, http.StatusConflict, codeConflict, "two-factor authentication is not enabled")
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, auth.ErrEmailTaken):
		writeError(w, http.StatusConflict, codeConflict, "a user with this email already exists")
	case errors.Is(err, auth.ErrLastAdmin):
		writeError(w, http.StatusConflict, codeConflict, "cannot demote or delete the last admin")
	case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrInvalidEmail), errors.Is(err, auth.ErrInvalidRole):
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
	default:
		slog.ErrorContext(ctx, "api: internal error", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}
