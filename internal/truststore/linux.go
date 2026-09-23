package truststore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// linuxStores enumerates the system anchor directory of the running
// distribution (what curl, Chrome through its system fallback, and most
// tools consult) and every NSS database on the machine (Chrome's own, and
// each Firefox profile), which need certutil from the NSS tools.
func linuxStores() []store {
	result := []store{linuxSystemAnchors()}
	for _, dir := range nssDatabases() {
		result = append(result, nssDatabase{dir: dir})
	}
	return result
}

// anchorLayouts are the distribution families' anchor directory and the
// command that rebuilds the trust bundle from it, probed in order.
var anchorLayouts = []struct {
	dir     string
	update  []string
	pattern string
}{
	{"/etc/pki/ca-trust/source/anchors", []string{"update-ca-trust", "extract"}, "%s.pem"},       // Fedora, RHEL
	{"/usr/local/share/ca-certificates", []string{"update-ca-certificates"}, "%s.crt"},           // Debian, Ubuntu (.crt required)
	{"/etc/ca-certificates/trust-source/anchors", []string{"trust", "extract-compat"}, "%s.pem"}, // Arch
	{"/usr/share/pki/trust/anchors", []string{"update-ca-certificates"}, "%s.pem"},               // openSUSE
}

// linuxAnchors is the distribution's system anchor directory.
type linuxAnchors struct {
	dir     string
	update  []string
	pattern string
}

func linuxSystemAnchors() store {
	for _, layout := range anchorLayouts {
		if info, err := os.Stat(filepath.Join(fsRoot, layout.dir)); err == nil && info.IsDir() {
			return linuxAnchors{dir: filepath.Join(fsRoot, layout.dir), update: layout.update, pattern: layout.pattern}
		}
	}
	return linuxAnchors{}
}

func (linuxAnchors) name() string { return "system trust anchors" }

// anchorFile names the CA inside the anchor directory: the store's file
// name convention around a slug of the CA's name, so two generations of
// the CA sit side by side instead of overwriting each other.
func (a linuxAnchors) anchorFile(certificate Certificate) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, certificate.Name)
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	return filepath.Join(a.dir, fmt.Sprintf(a.pattern, strings.Trim(slug, "-")))
}

func (a linuxAnchors) check(_ context.Context, certificate Certificate) (State, string, error) {
	if a.dir == "" {
		return StateUnavailable, "no known system anchor directory on this distribution; import the CA by hand", nil
	}
	existing, err := os.ReadFile(a.anchorFile(certificate))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return StateMissing, "", nil
	case err != nil:
		return StateMissing, "", err
	case bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(certificate.PEM)):
		return StateTrusted, "", nil
	default:
		return StateMissing, "", nil
	}
}

// install copies the certificate into the anchor directory and rebuilds
// the bundle, through sudo unless already root.
func (a linuxAnchors) install(ctx context.Context, certificate Certificate) error {
	if a.dir == "" {
		return errors.New("no known system anchor directory on this distribution")
	}
	target := a.anchorFile(certificate)
	copyArgs := []string{"cp", certificate.Path, target}
	updateArgs := a.update
	if !isRoot() {
		if _, err := lookPath("sudo"); err != nil {
			return errors.New("sudo is not available; as root, copy " + certificate.Path + " to " + target + " and run " + strings.Join(a.update, " "))
		}
		copyArgs = append([]string{"sudo"}, copyArgs...)
		updateArgs = append([]string{"sudo"}, updateArgs...)
	}
	if out, err := run(ctx, copyArgs[0], copyArgs[1:]...); err != nil {
		return toolError(strings.Join(copyArgs, " "), out, err)
	}
	if out, err := run(ctx, updateArgs[0], updateArgs[1:]...); err != nil {
		return toolError(strings.Join(updateArgs, " "), out, err)
	}
	return nil
}

// nssDatabases finds the NSS databases browsers keep in the home
// directory: Chrome's shared one, and every Firefox profile (including the
// snap's).
func nssDatabases() []string {
	home, err := homeDir()
	if err != nil || home == "" {
		return nil
	}
	var dirs []string
	if _, err := os.Stat(filepath.Join(home, ".pki", "nssdb")); err == nil {
		dirs = append(dirs, filepath.Join(home, ".pki", "nssdb"))
	}
	for _, pattern := range []string{
		filepath.Join(home, ".mozilla", "firefox", "*"),
		filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox", "*"),
	} {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			if _, err := os.Stat(filepath.Join(match, "cert9.db")); err == nil {
				dirs = append(dirs, match)
			}
		}
	}
	return dirs
}

// nssDatabase is one NSS certificate database, driven through certutil.
type nssDatabase struct{ dir string }

func (n nssDatabase) name() string {
	home, _ := homeDir()
	if rel, err := filepath.Rel(home, n.dir); err == nil && home != "" {
		return "NSS database ~/" + rel
	}
	return "NSS database " + n.dir
}

func (n nssDatabase) check(ctx context.Context, certificate Certificate) (State, string, error) {
	if _, err := lookPath("certutil"); err != nil {
		return StateUnavailable, "install certutil (libnss3-tools or nss-tools) to trust the CA in Chrome and Firefox", nil
	}
	if _, err := run(ctx, "certutil", "-d", "sql:"+n.dir, "-L", "-n", certificate.Name); err != nil {
		return StateMissing, "", nil
	}
	return StateTrusted, "", nil
}

func (n nssDatabase) install(ctx context.Context, certificate Certificate) error {
	out, err := run(ctx, "certutil", "-d", "sql:"+n.dir, "-A", "-t", "C,,", "-n", certificate.Name, "-i", certificate.Path)
	if err != nil {
		return toolError("certutil -A", out, err)
	}
	return nil
}
