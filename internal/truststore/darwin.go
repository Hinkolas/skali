package truststore

import (
	"context"
	"errors"
	"strings"
)

// darwinKeychain is the user's login keychain, which Safari, Chrome, and
// Firefox all consult. Writing it needs no sudo: macOS asks for the login
// password in a dialog once. The system keychain would need sudo in every
// developer's terminal for a CA that is personal to this user anyway.
type darwinKeychain struct{}

func (darwinKeychain) name() string { return "macOS login keychain" }

// check asks the security framework to verify the CA certificate itself:
// that succeeds exactly when a trust setting for it exists in one of the
// user's keychains (or the system's), which is what browsers will see.
func (darwinKeychain) check(ctx context.Context, certificate Certificate) (State, string, error) {
	if _, err := lookPath("security"); err != nil {
		return StateUnavailable, "the security tool is not on PATH", nil
	}
	if _, err := run(ctx, "security", "verify-cert", "-c", certificate.Path, "-L"); err != nil {
		return StateMissing, "", nil
	}
	return StateTrusted, "", nil
}

func (darwinKeychain) install(ctx context.Context, certificate Certificate) error {
	out, err := run(ctx, "security", "login-keychain")
	if err != nil {
		return toolError("security login-keychain", out, err)
	}
	keychain := strings.Trim(strings.TrimSpace(string(out)), `"`)
	if keychain == "" {
		return errors.New("security login-keychain named no keychain")
	}
	out, err = run(ctx, "security", "add-trusted-cert", "-r", "trustRoot", "-k", keychain, certificate.Path)
	if err != nil {
		return toolError("security add-trusted-cert", out, err)
	}
	return nil
}
