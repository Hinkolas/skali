package installer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

func TestNormalizeJoinServer(t *testing.T) {
	t.Parallel()
	valid, err := NormalizeJoinServer("https://10.1.0.3:6443")
	require.NoError(t, err)
	require.Equal(t, "https://10.1.0.3:6443", valid)

	for _, value := range []string{
		"http://10.1.0.3:6443",
		"https://user:pass@10.1.0.3:6443",
		"https://10.1.0.3:6443/cacerts",
		"https://10.1.0.3:6443?x=1",
		"https://10.1.0.3:6443#fragment",
	} {
		_, err := NormalizeJoinServer(value)
		require.Error(t, err, value)
	}
}

func TestValidateEnrolledHostDoesNotRequireK3sJoinInputs(t *testing.T) {
	t.Parallel()
	fake := linuxHost()

	err := ValidateEnrolledHost(context.Background(), fake, "e2e", layout.RoleAgent,
		"worker-1", NodeNetwork{}, []string{layout.CapabilityApplication})

	require.NoError(t, err)
	require.Empty(t, fake.Writes)
	require.Empty(t, fake.Requests)
}

func TestInstallWrongJoinEndpointMakesNoChanges(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	token := secureTestToken(t, fake, "server:secret")
	fake.HTTPHandler = func(host.HTTPRequest) (host.HTTPResponse, error) {
		return host.HTTPResponse{}, errors.New("dial tcp 10.1.0.4:6443: connect: connection refused")
	}

	_, err := Install(context.Background(), fake, InstallOptions{
		Cluster:      "e2e",
		Role:         layout.RoleServer,
		Capabilities: []string{layout.CapabilityApplication},
		Join: &JoinOptions{
			Server: "https://10.1.0.4:6443",
			Token: encodeJoinTokenWithClaims(
				token, "pull", layout.RoleServer, "e2e", "https://10.1.0.3:6443"),
		},
	})
	require.ErrorContains(t, err, "connection was refused")
	require.ErrorContains(t, err, "No changes were made.")
	require.Empty(t, fake.Writes)
	for _, command := range fake.Commands {
		require.NotEqual(t, "sh", command.Name)
	}
}

func TestInstallRejectedCredentialMakesNoChanges(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	token := secureTestToken(t, fake, "abcdef.abcdefghijklmnop")
	original := fake.HTTPHandler
	fake.HTTPHandler = func(request host.HTTPRequest) (host.HTTPResponse, error) {
		if strings.HasSuffix(request.URL, "/cacerts") {
			return original(request)
		}
		return host.HTTPResponse{StatusCode: 401}, nil
	}

	_, err := Install(context.Background(), fake, InstallOptions{
		Cluster:      "e2e",
		Role:         layout.RoleAgent,
		Capabilities: []string{layout.CapabilityDatabase},
		Join:         &JoinOptions{Server: "https://10.1.0.3:6443", Token: token},
	})
	require.ErrorContains(t, err, "credential is invalid or expired")
	require.ErrorContains(t, err, "No changes were made.")
	require.Empty(t, fake.Writes)
}

func TestInstallRejectsInsecureAndMalformedJoinInputsWithoutMutation(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		token    string
		server   string
		hostname string
		want     string
	}{
		"short token": {
			token: "server:secret", server: "https://10.1.0.3:6443",
			want: "not in secure K10 format",
		},
		"credential line break": {
			token:  "K10" + strings.Repeat("a", 64) + "::server:secret\nheader = \"x\"",
			server: "https://10.1.0.3:6443", want: "line break",
		},
		"invalid origin": {
			token:  "K10" + strings.Repeat("a", 64) + "::server:secret",
			server: "https://10.1.0.3:6443/path", want: "without credentials, path, query, or fragment",
		},
		"invalid hostname": {
			token:  "K10" + strings.Repeat("a", 64) + "::server:secret",
			server: "https://10.1.0.3:6443", hostname: "NOT_VALID", want: "not a valid Kubernetes node name",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := linuxHost()
			if testCase.hostname != "" {
				fake.Handlers["hostname"] = func(host.Command) (host.Result, error) {
					return host.Result{Stdout: testCase.hostname + "\n"}, nil
				}
			}
			_, err := Install(context.Background(), fake, InstallOptions{
				Cluster:      "e2e",
				Role:         layout.RoleServer,
				Capabilities: []string{layout.CapabilityApplication},
				Join:         &JoinOptions{Server: testCase.server, Token: testCase.token},
			})
			require.ErrorContains(t, err, testCase.want)
			require.ErrorContains(t, err, "No changes were made.")
			require.Empty(t, fake.Writes)
			require.Empty(t, fake.Requests)
		})
	}
}

func TestInstallRejectsMalformedAndMismatchedClusterCA(t *testing.T) {
	t.Parallel()
	t.Run("malformed CA", func(t *testing.T) {
		t.Parallel()
		fake := linuxHost()
		fake.HTTPHandler = func(host.HTTPRequest) (host.HTTPResponse, error) {
			return host.HTTPResponse{StatusCode: 200, Body: []byte("not a certificate")}, nil
		}
		_, err := Install(context.Background(), fake, InstallOptions{
			Cluster: "e2e", Role: layout.RoleAgent,
			Capabilities: []string{layout.CapabilityDatabase},
			Join: &JoinOptions{
				Server: "https://10.1.0.3:6443",
				Token:  "K10" + strings.Repeat("a", 64) + "::abcdef.abcdefghijklmnop",
			},
		})
		require.ErrorContains(t, err, "malformed CA bundle")
		require.Empty(t, fake.Writes)
	})
	t.Run("CA mismatch", func(t *testing.T) {
		t.Parallel()
		fake := linuxHost()
		first := secureTestToken(t, fake, "abcdef.abcdefghijklmnop")
		_ = secureTestToken(t, fake, "abcdef.abcdefghijklmnop") // replaces the served CA
		_, err := Install(context.Background(), fake, InstallOptions{
			Cluster: "e2e", Role: layout.RoleAgent,
			Capabilities: []string{layout.CapabilityDatabase},
			Join:         &JoinOptions{Server: "https://10.1.0.3:6443", Token: first},
		})
		require.ErrorContains(t, err, "token CA hash does not match")
		require.Empty(t, fake.Writes)
	})
}

func TestJoinPreflightUsesRoleSpecificAuthenticatedEndpoint(t *testing.T) {
	t.Parallel()
	for _, role := range []string{layout.RoleAgent, layout.RoleServer} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			fake := linuxHost()
			credentials := "abcdef.abcdefghijklmnop"
			wantPath := "/v1-k3s/config"
			if role == layout.RoleServer {
				credentials = "server:secret"
				wantPath = "/v1-k3s/server-bootstrap"
			}
			token := secureTestToken(t, fake, credentials)
			_, err := resolveAndPreflightInstall(context.Background(), fake,
				&Host{Hostname: "cp-1"}, InstallOptions{
					Cluster: "e2e", Role: role,
					Capabilities: []string{layout.CapabilityApplication},
					Join:         &JoinOptions{Server: "https://10.1.0.3:6443", Token: token},
				})
			require.NoError(t, err)
			require.Len(t, fake.Requests, 2)
			require.True(t, strings.HasSuffix(fake.Requests[1].URL, wantPath))
			if role == layout.RoleAgent {
				require.Equal(t, credentials, fake.Requests[1].Bearer)
				require.Empty(t, fake.Requests[1].Password)
			} else {
				require.Equal(t, "server", fake.Requests[1].Username)
				require.Equal(t, "secret", fake.Requests[1].Password)
			}
		})
	}
}

func TestInstallRejectsTokenClaimMismatchBeforeProbe(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	token := secureTestToken(t, fake, "server:secret")
	composite := encodeJoinTokenWithClaims(token, "pull", layout.RoleServer, "production",
		"https://10.1.0.3:6443")

	_, err := Install(context.Background(), fake, InstallOptions{
		Cluster:      "staging",
		Role:         layout.RoleServer,
		Capabilities: []string{layout.CapabilityApplication},
		Join:         &JoinOptions{Token: composite},
	})
	require.ErrorContains(t, err, `belongs to cluster "production", not "staging"`)
	require.Empty(t, fake.Requests)
	require.Empty(t, fake.Writes)
}

func TestOrphanRecoveryValidatesMatchingIdentityBeforeAdoption(t *testing.T) {
	t.Parallel()
	fake := withK3s(linuxHost(), "k3s.service", false)
	fake.FS[K3sConfigPath] = []byte(k3sConfigYAML(k3sNode{
		Name: "cp-1", Cluster: "e2e", Role: layout.RoleServer,
		Capabilities: []string{layout.CapabilityApplication},
		ServerURL:    "https://10.1.0.3:6443",
	}))
	fake.FS[K3sRegistriesPath] = []byte(k3sRegistriesYAML("pull"))
	token := secureTestToken(t, fake, "server:secret")

	_, err := Install(context.Background(), fake, InstallOptions{
		Cluster:       "other",
		Role:          layout.RoleServer,
		Capabilities:  []string{layout.CapabilityApplication},
		Join:          &JoinOptions{Server: "https://10.1.0.3:6443", Token: token},
		RecoverOrphan: true,
	})
	require.ErrorContains(t, err, `cannot change cluster "e2e"/server to "other"/server`)
	require.ErrorContains(t, err, "No changes were made.")
	_, exists := fake.FS[RecordPath]
	require.False(t, exists)
	detected, detectErr := Detect(context.Background(), fake)
	require.NoError(t, detectErr)
	require.Equal(t, StateOrphaned, detected.State)
}

func TestInstallPreStartFailureRollsBackToFresh(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	fake.Handlers["sh"] = func(host.Command) (host.Result, error) {
		return host.Result{ExitCode: 1, Stderr: "download failed"}, nil
	}

	_, err := Install(context.Background(), fake, InstallOptions{
		Cluster:      "e2e",
		Capabilities: []string{layout.CapabilityApplication},
	})
	require.ErrorContains(t, err, "was rolled back")
	detected, detectErr := Detect(context.Background(), fake)
	require.NoError(t, detectErr)
	require.Equal(t, StateFresh, detected.State)
}

func TestInstallStartFailureStaysManagedAndRedactsSecrets(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	token := secureTestToken(t, fake, "server:super-secret-token")
	pullSecret := "super-secret-pull"
	fake.Handlers["sh"] = func(host.Command) (host.Result, error) {
		fake.FS[K3sBinaryPath] = []byte("binary")
		fake.FS[k3sUninstallScript] = []byte("script")
		return host.Result{}, nil
	}
	fake.Handlers["systemctl"] = func(command host.Command) (host.Result, error) {
		if len(command.Args) > 0 && command.Args[0] == "start" {
			return host.Result{ExitCode: 1, Stderr: "unit failed"}, nil
		}
		if len(command.Args) > 0 && command.Args[0] == "enable" {
			return host.Result{}, nil
		}
		if len(command.Args) > 0 && command.Args[0] == "status" {
			return host.Result{ExitCode: 3, Stdout: "failed " + token}, nil
		}
		return host.Result{ExitCode: 4, Stdout: "not-found"}, nil
	}
	fake.Handlers["apt-get"] = func(host.Command) (host.Result, error) {
		return host.Result{}, nil
	}
	fake.Handlers["journalctl"] = func(host.Command) (host.Result, error) {
		return host.Result{Stdout: `level=fatal msg="bad credential ` + pullSecret + `"`}, nil
	}

	_, err := Install(context.Background(), fake, InstallOptions{
		Cluster:      "e2e",
		Role:         layout.RoleServer,
		Capabilities: []string{layout.CapabilityApplication},
		Join: &JoinOptions{
			Server: "https://10.1.0.3:6443",
			Token: encodeJoinTokenWithClaims(
				token, pullSecret, layout.RoleServer, "e2e", "https://10.1.0.3:6443"),
		},
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), token)
	require.NotContains(t, err.Error(), pullSecret)
	require.ErrorContains(t, err, "remains managed")

	record, loadErr := LoadRecord(context.Background(), fake)
	require.NoError(t, loadErr)
	require.Equal(t, InstallStatusFailed, record.Lifecycle.Status)
	require.True(t, record.Lifecycle.StartAttempted)
	require.NotContains(t, record.Lifecycle.LastError, token)
	require.NotContains(t, record.Lifecycle.LastError, pullSecret)
	logData, readErr := fake.ReadFile(context.Background(), record.Lifecycle.LastLog)
	require.NoError(t, readErr)
	require.NotContains(t, string(logData), token)
	require.NotContains(t, string(logData), pullSecret)

	detected, detectErr := Detect(context.Background(), fake)
	require.NoError(t, detectErr)
	require.Equal(t, StateInterrupted, detected.State)
}

func TestInterruptedJoinResumesWithCorrectedEndpoint(t *testing.T) {
	t.Parallel()
	fake := installReadyHost([]string{layout.CapabilityApplication})
	token := secureTestToken(t, fake, "server:secret")
	startCalls := 0
	started := false
	fake.Handlers["systemctl"] = func(command host.Command) (host.Result, error) {
		if len(command.Args) == 0 {
			return host.Result{ExitCode: 1}, nil
		}
		switch command.Args[0] {
		case "start":
			startCalls++
			if startCalls == 1 {
				return host.Result{ExitCode: 1, Stderr: "injected start failure"}, nil
			}
			started = true
			return host.Result{}, nil
		case "stop":
			started = false
			return host.Result{}, nil
		case "enable":
			return host.Result{}, nil
		case "status":
			return host.Result{ExitCode: 3, Stdout: "failed"}, nil
		case "is-enabled":
			if _, installed := fake.FS[K3sBinaryPath]; installed {
				return host.Result{Stdout: "enabled"}, nil
			}
			return host.Result{ExitCode: 1, Stdout: "not-found"}, nil
		case "is-active":
			if started {
				return host.Result{Stdout: "active"}, nil
			}
			return host.Result{ExitCode: 3, Stdout: "inactive"}, nil
		}
		return host.Result{ExitCode: 1}, nil
	}
	fake.Handlers["journalctl"] = func(host.Command) (host.Result, error) {
		return host.Result{Stdout: `level=fatal msg="injected start failure"`}, nil
	}
	composite := encodeJoinTokenWithClaims(token, "pull", layout.RoleServer, "e2e",
		"https://10.1.0.4:6443")

	_, err := Install(context.Background(), fake, InstallOptions{
		Cluster:      "e2e",
		Role:         layout.RoleServer,
		Capabilities: []string{layout.CapabilityApplication},
		Join:         &JoinOptions{Server: "https://10.1.0.4:6443", Token: composite},
	})
	require.Error(t, err)
	failed, err := LoadRecord(context.Background(), fake)
	require.NoError(t, err)
	installationID := failed.InstallationID
	require.Equal(t, "https://10.1.0.4:6443", failed.Join.Server)

	completed, err := Install(context.Background(), fake, InstallOptions{
		Cluster:      "e2e",
		Role:         layout.RoleServer,
		Capabilities: []string{layout.CapabilityApplication},
		Join:         &JoinOptions{Server: "https://10.1.0.3:6443", Token: composite},
	})
	require.NoError(t, err)
	require.Equal(t, installationID, completed.InstallationID)
	require.Equal(t, InstallStatusComplete, completed.Lifecycle.Status)
	require.Equal(t, "https://10.1.0.3:6443", completed.Join.Server)
	require.GreaterOrEqual(t, startCalls, 2)
}
