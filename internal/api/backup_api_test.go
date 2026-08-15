package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func validBackupTarget() map[string]string {
	return map[string]string{
		"endpoint":          "https://s3.example.test",
		"region":            "eu-central-1",
		"bucket":            "skali-backups",
		"prefix":            "prod",
		"access_key_id":     "AKIDEXAMPLE",
		"secret_access_key": "topsecretkey",
	}
}

func TestBackupTargetRequireAdmin(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	a.createUser("member@example.com", "hunter2hunter2")
	member := a.login("member@example.com", "hunter2hunter2")

	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/system/backup-target"},
		{"PUT", "/v1/system/backup-target"},
		{"DELETE", "/v1/system/backup-target"},
	} {
		status, body := a.do(tc.method, tc.path, member, nil)
		require.Equal(t, http.StatusForbidden, status, "%s %s", tc.method, tc.path)
		require.Equal(t, "forbidden", errorCode(t, body))
	}
}

func TestBackupTargetRequiresFreshAuth(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")
	a.staleAllSessions()

	status, body := a.do("PUT", "/v1/system/backup-target", admin, validBackupTarget())
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
}

func TestBackupTargetPutAndGet(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")

	// Unconfigured reads are a 404, not an empty object.
	status, body := a.do("GET", "/v1/system/backup-target", admin, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))

	status, body = a.do("PUT", "/v1/system/backup-target", admin, validBackupTarget())
	require.Equal(t, http.StatusOK, status, "body: %v", body)
	target := body["target"].(map[string]any)
	require.Equal(t, "default", target["name"])
	require.Equal(t, "https://s3.example.test", target["endpoint"])
	require.Equal(t, "AKIDEXAMPLE", target["access_key_id"])
	// The secret is write-only: absent from the PUT echo and every read.
	require.NotContains(t, target, "secret_access_key")

	status, body = a.do("GET", "/v1/system/backup-target", admin, nil)
	require.Equal(t, http.StatusOK, status)
	target = body["target"].(map[string]any)
	require.Equal(t, "skali-backups", target["bucket"])
	require.Equal(t, "prod", target["prefix"])
	require.NotContains(t, target, "secret_access_key")

	// A second PUT replaces the whole target.
	replacement := validBackupTarget()
	replacement["bucket"] = "other-bucket"
	status, body = a.do("PUT", "/v1/system/backup-target", admin, replacement)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "other-bucket", body["target"].(map[string]any)["bucket"])
}

func TestBackupTargetDelete(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")

	// Deleting an unconfigured target is a 404, matching reads.
	status, body := a.do("DELETE", "/v1/system/backup-target", admin, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))

	status, _ = a.do("PUT", "/v1/system/backup-target", admin, validBackupTarget())
	require.Equal(t, http.StatusOK, status)

	status, _ = a.do("DELETE", "/v1/system/backup-target", admin, nil)
	require.Equal(t, http.StatusNoContent, status)

	// The target is gone for reads and repeat deletes alike.
	status, body = a.do("GET", "/v1/system/backup-target", admin, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))
	status, _ = a.do("DELETE", "/v1/system/backup-target", admin, nil)
	require.Equal(t, http.StatusNotFound, status)
}

func TestBackupTargetValidation(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")

	for name, mutate := range map[string]func(map[string]string){
		"endpoint not a URL": func(m map[string]string) { m["endpoint"] = "s3.example.test" },
		"empty bucket":       func(m map[string]string) { m["bucket"] = "" },
		"empty access key":   func(m map[string]string) { m["access_key_id"] = "" },
		"empty secret key":   func(m map[string]string) { m["secret_access_key"] = "" },
	} {
		payload := validBackupTarget()
		mutate(payload)
		status, _ := a.do("PUT", "/v1/system/backup-target", admin, payload)
		require.Equal(t, http.StatusUnprocessableEntity, status, name)
	}

	// Unknown fields are rejected by the strict decoder.
	payload := map[string]string{"endpoint": "https://x", "bucket": "b",
		"access_key_id": "a", "secret_access_key": "s", "nope": "x"}
	status, body := a.do("PUT", "/v1/system/backup-target", admin, payload)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))
}

func TestBackupCreatePreconditions(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("dev@example.com", "hunter2hunter2")
	a.createAdmin("admin@example.com", "hunter2hunter2")
	token := a.login("dev@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	// Unknown environment and project.
	status, body := a.do("POST", "/v1/environments/00000000-0000-0000-0000-000000000000/backups", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))
	status, body = a.do("GET", "/v1/projects/00000000-0000-0000-0000-000000000000/backups", token, nil)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "not_found", errorCode(t, body))

	// An environment that never deployed is not active.
	status, body = a.do("POST", "/v1/environments/"+envID+"/backups", token, nil)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "environment_not_active", errorCode(t, body))

	// Listing snapshots without a configured target is a 503 with a
	// distinct code, not an opaque failure.
	status, body = a.do("GET", "/v1/environments/"+envID+"/backups", token, nil)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "backup_target_unconfigured", errorCode(t, body))
	status, body = a.do("GET", "/v1/projects/"+projectID+"/backups", token, nil)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "backup_target_unconfigured", errorCode(t, body))

	// With a target configured but unreachable, listing reports it.
	status, _ = a.do("PUT", "/v1/system/backup-target", admin, map[string]string{
		"endpoint": "http://127.0.0.1:1", "bucket": "backups",
		"access_key_id": "AK", "secret_access_key": "sk",
	})
	require.Equal(t, http.StatusOK, status)
	status, body = a.do("GET", "/v1/environments/"+envID+"/backups", token, nil)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "backup_target_unreachable", errorCode(t, body))
	status, body = a.do("GET", "/v1/projects/"+projectID+"/backups", token, nil)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "backup_target_unreachable", errorCode(t, body))
}

func TestRestorePreconditions(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("dev@example.com", "hunter2hunter2")
	token := a.login("dev@example.com", "hunter2hunter2")
	_, envID := a.createEnvironment(t, token)

	// Restore is destructive: a stale sudo window is refused before any
	// other validation.
	a.staleAllSessions()
	status, body := a.do("POST", "/v1/environments/"+envID+"/restore", token,
		map[string]string{"snapshot_id": "some-id"})
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
}

func TestRestoreValidation(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("dev@example.com", "hunter2hunter2")
	token := a.login("dev@example.com", "hunter2hunter2")
	_, envID := a.createEnvironment(t, token)

	// Missing snapshot id.
	status, body := a.do("POST", "/v1/environments/"+envID+"/restore", token, map[string]string{})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	// A never-deployed environment cannot be restored into.
	status, body = a.do("POST", "/v1/environments/"+envID+"/restore", token,
		map[string]string{"snapshot_id": "some-id"})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "environment_not_active", errorCode(t, body))
}
