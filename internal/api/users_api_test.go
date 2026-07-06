package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsersRequireAdmin(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	a.createUser("member@example.com", "hunter2hunter2")
	member := a.login("member@example.com", "hunter2hunter2")

	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/users"},
		{"POST", "/v1/users"},
		{"PATCH", "/v1/users/00000000-0000-0000-0000-000000000000"},
		{"DELETE", "/v1/users/00000000-0000-0000-0000-000000000000"},
		{"POST", "/v1/users/00000000-0000-0000-0000-000000000000/password"},
	} {
		status, body := a.do(tc.method, tc.path, member, nil)
		require.Equal(t, http.StatusForbidden, status, "%s %s", tc.method, tc.path)
		require.Equal(t, "forbidden", errorCode(t, body))
	}
}

func TestUsersListAndCreate(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")

	// Role defaults to member when omitted.
	status, body := a.do("POST", "/v1/users", admin, map[string]string{
		"email": "dev@example.com", "name": "Dev", "password": "hunter2hunter2",
	})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	created := body["user"].(map[string]any)
	require.Equal(t, "member", created["role"])
	require.Equal(t, "Dev", created["name"])

	// Explicit admin role.
	status, body = a.do("POST", "/v1/users", admin, map[string]string{
		"email": "root@example.com", "password": "hunter2hunter2", "role": "admin",
	})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	require.Equal(t, "admin", body["user"].(map[string]any)["role"])

	// Duplicate email is a conflict, unknown role a bad request.
	status, body = a.do("POST", "/v1/users", admin, map[string]string{
		"email": "dev@example.com", "password": "hunter2hunter2",
	})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	status, body = a.do("POST", "/v1/users", admin, map[string]string{
		"email": "x@example.com", "password": "hunter2hunter2", "role": "owner",
	})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	// The new users can log in, and the list shows everyone with roles.
	a.login("dev@example.com", "hunter2hunter2")
	status, body = a.do("GET", "/v1/users", admin, nil)
	require.Equal(t, http.StatusOK, status)
	users := body["users"].([]any)
	require.Len(t, users, 3)
	for _, u := range users {
		require.Contains(t, []string{"admin", "member"}, u.(map[string]any)["role"])
	}
}

func TestUsersUpdate(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	a.createUser("dev@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")

	devID := a.userID(admin, "dev@example.com")
	adminID := a.userID(admin, "admin@example.com")

	// Rename and promote in one request.
	status, body := a.do("PATCH", "/v1/users/"+devID, admin, map[string]string{
		"name": "Dev One", "role": "admin",
	})
	require.Equal(t, http.StatusOK, status, "body: %v", body)
	user := body["user"].(map[string]any)
	require.Equal(t, "Dev One", user["name"])
	require.Equal(t, "admin", user["role"])

	// Empty patch is a bad request.
	status, body = a.do("PATCH", "/v1/users/"+devID, admin, map[string]string{})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	// Changing your own role is refused, even with another admin around.
	status, body = a.do("PATCH", "/v1/users/"+adminID, admin, map[string]string{"role": "member"})
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	// Demote the other admin back — allowed, one admin remains.
	status, _ = a.do("PATCH", "/v1/users/"+devID, admin, map[string]string{"role": "member"})
	require.Equal(t, http.StatusOK, status)

	// Unknown user id.
	status, _ = a.do("PATCH", "/v1/users/00000000-0000-0000-0000-000000000000", admin, map[string]string{"role": "member"})
	require.Equal(t, http.StatusNotFound, status)
}

func TestUsersDelete(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	a.createUser("dev@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")
	devToken := a.login("dev@example.com", "hunter2hunter2")

	adminID := a.userID(admin, "admin@example.com")
	devID := a.userID(admin, "dev@example.com")

	// Self-deletion is refused.
	status, body := a.do("DELETE", "/v1/users/"+adminID, admin, nil)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "conflict", errorCode(t, body))

	// Deleting a member kills their sessions via cascade.
	status, _ = a.do("DELETE", "/v1/users/"+devID, admin, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, _ = a.do("GET", "/v1/auth/session", devToken, nil)
	require.Equal(t, http.StatusUnauthorized, status)

	status, _ = a.do("DELETE", "/v1/users/"+devID, admin, nil)
	require.Equal(t, http.StatusNotFound, status)
}

func TestUsersResetPassword(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	a.createUser("dev@example.com", "old-password")
	admin := a.login("admin@example.com", "hunter2hunter2")
	devToken := a.login("dev@example.com", "old-password")

	devID := a.userID(admin, "dev@example.com")

	status, body := a.do("POST", "/v1/users/"+devID+"/password", admin, map[string]string{"new_password": "short"})
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))

	status, _ = a.do("POST", "/v1/users/"+devID+"/password", admin, map[string]string{"new_password": "new-password"})
	require.Equal(t, http.StatusNoContent, status)

	// Every session of the user is revoked; the new password works.
	status, _ = a.do("GET", "/v1/auth/session", devToken, nil)
	require.Equal(t, http.StatusUnauthorized, status)
	a.login("dev@example.com", "new-password")
}

// userID looks a user's id up via the list endpoint.
func (a *testAPI) userID(adminToken, email string) string {
	a.t.Helper()
	status, body := a.do("GET", "/v1/users", adminToken, nil)
	require.Equal(a.t, http.StatusOK, status)
	for _, u := range body["users"].([]any) {
		um := u.(map[string]any)
		if um["email"] == email {
			return um["id"].(string)
		}
	}
	a.t.Fatalf("no user %s in list", email)
	return ""
}
