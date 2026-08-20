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

// The user directory: readable by everyone authenticated, trimmed for
// non-admins, filtered by q. It backs the console's add-member picker and
// the admin user search.
func TestUsersDirectory(t *testing.T) {
	a := newTestAPI(t)
	a.createAdmin("admin@example.com", "hunter2hunter2")
	a.createMember("bob@example.com", "hunter2hunter2")
	admin := a.login("admin@example.com", "hunter2hunter2")
	bob := a.login("bob@example.com", "hunter2hunter2")

	// A member reads the directory subset: no account fields.
	status, body := a.do("GET", "/v1/users", bob, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Len(t, body["users"], 2)
	entry := body["users"].([]any)[0].(map[string]any)
	require.Subset(t, entry, map[string]any{"email": "admin@example.com", "role": "admin"})
	require.NotContains(t, entry, "create_projects")
	require.NotContains(t, entry, "two_factor_enabled")
	require.NotContains(t, entry, "created_at")

	// Admins keep the full account payload, q filters for both.
	status, body = a.do("GET", "/v1/users", admin, nil)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body["users"].([]any)[0].(map[string]any), "create_projects")
	status, body = a.do("GET", "/v1/users?q=BOB", admin, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["users"], 1)
	require.Equal(t, "bob@example.com", body["users"].([]any)[0].(map[string]any)["email"])

	// The name matches too; ILIKE metacharacters stay literal.
	status, body = a.do("PATCH", "/v1/users/"+body["users"].([]any)[0].(map[string]any)["id"].(string), admin, map[string]any{"name": "Robert Tables"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = a.do("GET", "/v1/users?q=robert", bob, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, body["users"], 1)
	status, body = a.do("GET", "/v1/users?q=%25", bob, nil)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, body["users"])
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
