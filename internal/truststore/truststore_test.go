package truststore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeTools struct {
	calls   []string
	answers map[string]struct {
		out []byte
		err error
	}
}

func (f *fakeTools) run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	for prefix, answer := range f.answers {
		if strings.HasPrefix(call, prefix) {
			return answer.out, answer.err
		}
	}
	return nil, nil
}

func (f *fakeTools) fail(prefix, output string) {
	if f.answers == nil {
		f.answers = map[string]struct {
			out []byte
			err error
		}{}
	}
	f.answers[prefix] = struct {
		out []byte
		err error
	}{[]byte(output), errors.New("exit status 1")}
}

func (f *fakeTools) answer(prefix, output string) {
	if f.answers == nil {
		f.answers = map[string]struct {
			out []byte
			err error
		}{}
	}
	f.answers[prefix] = struct {
		out []byte
		err error
	}{[]byte(output), nil}
}

func seams(t *testing.T, os_ string, tools *fakeTools, onPath ...string) {
	t.Helper()
	origRun, origLook, origRoot, origHome, origOS, origIsRoot := run, lookPath, fsRoot, homeDir, goos, isRoot
	t.Cleanup(func() {
		run, lookPath, fsRoot, homeDir, goos, isRoot = origRun, origLook, origRoot, origHome, origOS, origIsRoot
	})
	run = tools.run
	lookPath = func(file string) (string, error) {
		for _, name := range onPath {
			if name == file {
				return "/usr/bin/" + file, nil
			}
		}
		return "", errors.New("not found")
	}
	goos = os_
	isRoot = func() bool { return false }
}

var testCert = Certificate{Path: "/state/ca.crt", PEM: []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"), Name: "skali local dev CA (skali-dev, 1a2b3c4d)"}

func TestDarwinLoginKeychain(t *testing.T) {
	tools := &fakeTools{}
	seams(t, "darwin", tools, "security")
	tools.fail("security verify-cert", "CSSMERR_TP_NOT_TRUSTED")
	tools.answer("security login-keychain", "    \"/Users/dev/Library/Keychains/login.keychain-db\"\n")

	status, err := Check(context.Background(), testCert)
	require.NoError(t, err)
	require.Equal(t, []StoreStatus{{Name: "macOS login keychain", State: StateMissing}}, status.Stores)
	require.False(t, status.Trusted())

	status, err = Install(context.Background(), testCert)
	require.NoError(t, err)
	require.True(t, status.Trusted())
	require.Equal(t, "installed now", status.Stores[0].Detail)
	require.Equal(t, []string{
		"security verify-cert -c /state/ca.crt -L",
		"security verify-cert -c /state/ca.crt -L",
		"security login-keychain",
		"security add-trusted-cert -r trustRoot -k /Users/dev/Library/Keychains/login.keychain-db /state/ca.crt",
	}, tools.calls)

	// A trusted CA is never written again, and a declined dialog reads as
	// failed with the tool's answer.
	tools.calls = nil
	tools.answers = nil
	status, err = Install(context.Background(), testCert)
	require.NoError(t, err)
	require.True(t, status.Trusted())
	require.Equal(t, []string{"security verify-cert -c /state/ca.crt -L"}, tools.calls)

	tools.fail("security verify-cert", "not trusted")
	tools.answer("security login-keychain", "\"/Users/dev/Library/Keychains/login.keychain-db\"")
	tools.fail("security add-trusted-cert", "SecTrustSettingsSetTrustSettings: The authorization was canceled by the user.")
	status, err = Install(context.Background(), testCert)
	require.NoError(t, err)
	require.False(t, status.Trusted())
	require.Equal(t, StateFailed, status.Stores[0].State)
	require.Contains(t, status.Stores[0].Detail, "canceled by the user")
}

func TestLinuxAnchorsAndNSSDatabases(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "usr/local/share/ca-certificates"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".pki/nssdb"), 0o755))
	profile := filepath.Join(home, ".mozilla/firefox/abcd.default-release")
	require.NoError(t, os.MkdirAll(profile, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profile, "cert9.db"), nil, 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".mozilla/firefox/no-db"), 0o755))

	tools := &fakeTools{}
	seams(t, "linux", tools, "sudo", "certutil")
	fsRoot = root
	homeDir = func() (string, error) { return home, nil }
	tools.fail("certutil -d sql:"+filepath.Join(home, ".pki/nssdb")+" -L", "not found")
	tools.fail("certutil -d sql:"+profile+" -L", "not found")

	status, err := Check(context.Background(), testCert)
	require.NoError(t, err)
	require.Len(t, status.Stores, 3, "the anchors, Chrome's database, and one Firefox profile")
	require.Equal(t, "system trust anchors", status.Stores[0].Name)
	require.Equal(t, StateMissing, status.Stores[0].State)
	require.Equal(t, "NSS database ~/.pki/nssdb", status.Stores[1].Name)
	require.Equal(t, "NSS database ~/.mozilla/firefox/abcd.default-release", status.Stores[2].Name)
	require.False(t, status.Trusted())

	status, err = Install(context.Background(), testCert)
	require.NoError(t, err)
	require.True(t, status.Trusted())
	anchor := filepath.Join(root, "usr/local/share/ca-certificates/skali-local-dev-ca-skali-dev-1a2b3c4d.crt")
	require.Contains(t, tools.calls, "sudo cp /state/ca.crt "+anchor, "Debian anchors need the .crt extension")
	require.Contains(t, tools.calls, "sudo update-ca-certificates")
	require.Contains(t, tools.calls, "certutil -d sql:"+profile+" -A -t C,, -n "+testCert.Name+" -i /state/ca.crt")
	require.Contains(t, tools.calls, "certutil -d sql:"+filepath.Join(home, ".pki/nssdb")+" -A -t C,, -n "+testCert.Name+" -i /state/ca.crt")

	// A byte-identical anchor reads as trusted without any tool call.
	require.NoError(t, os.WriteFile(anchor, testCert.PEM, 0o644))
	tools.calls = nil
	tools.answers = nil
	status, err = Check(context.Background(), testCert)
	require.NoError(t, err)
	require.Equal(t, StateTrusted, status.Stores[0].State)
	require.True(t, status.Trusted())
	require.NotContains(t, strings.Join(tools.calls, "\n"), "cp ")
}

func TestLinuxWithoutCertutilOrKnownDistribution(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/pki/ca-trust/source/anchors"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".pki/nssdb"), 0o755))
	tools := &fakeTools{}
	seams(t, "linux", tools, "sudo")
	fsRoot = root
	homeDir = func() (string, error) { return home, nil }

	status, err := Install(context.Background(), testCert)
	require.NoError(t, err)
	require.Equal(t, StateTrusted, status.Stores[0].State)
	require.Contains(t, tools.calls, "sudo update-ca-trust extract")
	require.Equal(t, StateUnavailable, status.Stores[1].State)
	require.Contains(t, status.Stores[1].Detail, "certutil")
	require.True(t, status.Trusted(), "an unreachable store does not block")

	// No known anchor directory: nothing reachable, so not trusted.
	fsRoot = t.TempDir()
	status, err = Check(context.Background(), testCert)
	require.NoError(t, err)
	require.Equal(t, StateUnavailable, status.Stores[0].State)
	require.False(t, status.Trusted())
}

func TestUnsupportedOS(t *testing.T) {
	tools := &fakeTools{}
	seams(t, "windows", tools)
	status, err := Install(context.Background(), testCert)
	require.NoError(t, err)
	require.False(t, status.Trusted())
	require.Equal(t, StateUnavailable, status.Stores[0].State)
	require.Empty(t, tools.calls)
}
