package installer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

// installReadyHost extends the fresh fake with a k3s install script
// handler and a node that comes up ready and labeled.
func installReadyHost(capabilities []string) *host.Fake {
	fake := linuxHost()
	started := false
	fake.Handlers["sh"] = func(host.Command) (host.Result, error) {
		fake.FS[K3sBinaryPath] = []byte("binary")
		fake.FS[k3sUninstallScript] = []byte("script")
		return host.Result{}, nil
	}
	fake.Handlers["systemctl"] = func(cmd host.Command) (host.Result, error) {
		if len(cmd.Args) > 0 && (cmd.Args[0] == "start" || cmd.Args[0] == "enable") {
			started = true
			return host.Result{}, nil
		}
		if started && len(cmd.Args) == 2 && cmd.Args[0] == "is-active" {
			return host.Result{Stdout: "active\n"}, nil
		}
		return host.Result{ExitCode: 4, Stdout: "not-found\n"}, nil
	}
	// A fresh host installs the Longhorn prerequisites through apt.
	fake.Handlers["apt-get"] = func(host.Command) (host.Result, error) {
		return host.Result{}, nil
	}
	fake.Handlers["k3s"] = func(cmd host.Command) (host.Result, error) {
		labels := layout.CapabilityLabels(capabilities)
		labels[layout.ClusterLabel] = "e2e"
		node := map[string]any{
			"items": []map[string]any{{
				"metadata": map[string]any{"name": "cp-1", "labels": labels},
				"status": map[string]any{"conditions": []map[string]any{
					{"type": "Ready", "status": "True"},
				}},
			}},
		}
		data, _ := json.Marshal(node)
		return host.Result{Stdout: string(data)}, nil
	}
	return fake
}

func secureTestToken(t *testing.T, fake *host.Fake, credentials string) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "k3s-test-ca"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	require.NoError(t, err)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	hash, err := hashK3sCA(ca)
	require.NoError(t, err)
	fake.HTTPHandler = func(request host.HTTPRequest) (host.HTTPResponse, error) {
		if strings.HasSuffix(request.URL, "/cacerts") {
			return host.HTTPResponse{StatusCode: 200, Body: ca}, nil
		}
		return host.HTTPResponse{StatusCode: 200, Body: []byte(`{}`)}, nil
	}
	return "K10" + hash + "::" + credentials
}

func TestInstallFreshServer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	capabilities := []string{layout.CapabilityApplication, layout.CapabilityEdge}
	fake := installReadyHost(capabilities)

	record, err := Install(ctx, fake, InstallOptions{
		Cluster:      "e2e",
		Capabilities: capabilities,
		Endpoints:    &Endpoints{API: "skali.e2e.test"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, record.InstallationID)
	require.Equal(t, "e2e", record.Cluster)
	require.Equal(t, layout.RoleServer, record.Node.Role)
	require.Equal(t, "cp-1", record.Node.Name)
	require.Equal(t, K3sVersion, record.Versions.K3s)
	require.Empty(t, record.Versions.Bundle, "install never initializes the bundle")

	// The record persisted and the host now detects as a managed server
	// once the units exist; with only the binary probe faked the state is
	// damaged-or-server territory, so just check the record loads.
	loaded, err := LoadRecord(ctx, fake)
	require.NoError(t, err)
	require.Equal(t, record.InstallationID, loaded.InstallationID)

	// The durable ownership record precedes config mutation; the first
	// server then mints the cluster's registry pull credential and
	// initializes the embedded etcd cluster.
	writes := fake.Writes
	require.Less(t, indexOf(writes, "replace "+RecordPath), indexOf(writes, "write "+K3sConfigPath))
	require.Contains(t, string(fake.FS[K3sConfigPath]), "cluster-init: true")
	require.Contains(t, string(fake.FS[K3sRegistriesPath]), "registry.skali.internal")
	require.NotEmpty(t, registriesPullSecret(fake.FS[K3sRegistriesPath]))
}

func TestInstallAgentJoin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := linuxHost()
	k3sToken := secureTestToken(t, fake, "abcdef.abcdefghijklmnop")
	fake.FS["/root/token"] = []byte(encodeJoinToken(k3sToken, "pull-secret-value", layout.RoleAgent) + "\n")
	joined := false
	fake.Handlers["sh"] = func(host.Command) (host.Result, error) {
		joined = true
		fake.FS[k3sAgentKubeletKubeconfig] = []byte("kubeconfig")
		return host.Result{}, nil
	}
	fake.Handlers["systemctl"] = func(cmd host.Command) (host.Result, error) {
		if len(cmd.Args) > 0 && (cmd.Args[0] == "start" || cmd.Args[0] == "enable") {
			joined = true
			return host.Result{}, nil
		}
		if joined && len(cmd.Args) == 2 && cmd.Args[0] == "is-active" && cmd.Args[1] == "k3s-agent.service" {
			return host.Result{Stdout: "active\n"}, nil
		}
		return host.Result{ExitCode: 4, Stdout: "not-found\n"}, nil
	}
	fake.Handlers["apt-get"] = func(host.Command) (host.Result, error) {
		return host.Result{}, nil
	}

	record, err := Install(ctx, fake, InstallOptions{
		Cluster:      "e2e",
		Role:         layout.RoleAgent,
		Capabilities: []string{layout.CapabilityDatabase},
		Join:         &JoinOptions{Server: "https://cp-1.internal:6443", TokenFile: "/root/token"},
	})
	require.NoError(t, err)
	require.Equal(t, layout.RoleAgent, record.Node.Role)
	require.Equal(t, "e2e", record.Cluster)
	require.Equal(t, []string{layout.CapabilityDatabase}, record.Node.Capabilities)
	require.NotNil(t, record.Join)
	require.Equal(t, "https://cp-1.internal:6443", record.Join.Server)
	require.Nil(t, record.Endpoints, "agents never gather endpoints")

	// The composite token splits: the k3s part lands in the token file,
	// the pull credential in registries.yaml.
	require.Equal(t, []byte(k3sToken+"\n"), fake.FS[K3sTokenPath])
	require.Contains(t, string(fake.FS[K3sConfigPath]), "server: https://cp-1.internal:6443")
	require.Equal(t, "pull-secret-value", registriesPullSecret(fake.FS[K3sRegistriesPath]))

	loaded, err := LoadRecord(ctx, fake)
	require.NoError(t, err)
	require.Equal(t, record.InstallationID, loaded.InstallationID)
	require.Equal(t, layout.RoleAgent, loaded.Node.Role)
}

func TestInstallAgentRequiresJoin(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	_, err := Install(context.Background(), fake, InstallOptions{
		Role:         layout.RoleAgent,
		Capabilities: []string{layout.CapabilityDatabase},
	})
	require.ErrorContains(t, err, "role agent requires join options")
	require.Empty(t, fake.Writes)
}

func TestInstallServerJoin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	capabilities := []string{layout.CapabilityApplication}
	fake := installReadyHost(capabilities)
	k3sToken := secureTestToken(t, fake, "server:secret")
	fake.FS["/root/token"] = []byte(encodeJoinToken(k3sToken, "pull-secret-value", layout.RoleServer) + "\n")
	joined := false
	fake.Handlers["sh"] = func(host.Command) (host.Result, error) {
		joined = true
		return host.Result{}, nil
	}
	fake.Handlers["systemctl"] = func(cmd host.Command) (host.Result, error) {
		if len(cmd.Args) > 0 && (cmd.Args[0] == "start" || cmd.Args[0] == "enable") {
			joined = true
			return host.Result{}, nil
		}
		if joined && len(cmd.Args) == 2 && cmd.Args[0] == "is-active" && cmd.Args[1] == "k3s.service" {
			return host.Result{Stdout: "active\n"}, nil
		}
		return host.Result{ExitCode: 4, Stdout: "not-found\n"}, nil
	}

	record, err := Install(ctx, fake, InstallOptions{
		Cluster:      "e2e",
		Role:         layout.RoleServer,
		Capabilities: capabilities,
		Join:         &JoinOptions{Server: "https://cp-1.internal:6443", TokenFile: "/root/token"},
	})
	require.NoError(t, err)
	require.Equal(t, layout.RoleServer, record.Node.Role)
	require.NotNil(t, record.Join)
	require.Equal(t, "https://cp-1.internal:6443", record.Join.Server)

	// A joining server references the existing server and never
	// initializes a new etcd cluster; the token and pull credential land
	// exactly like an agent join.
	config := string(fake.FS[K3sConfigPath])
	require.Contains(t, config, "server: https://cp-1.internal:6443")
	require.Contains(t, config, "token-file: "+K3sTokenPath)
	require.Contains(t, config, "embedded-registry: true")
	require.NotContains(t, config, "cluster-init")
	require.Equal(t, []byte(k3sToken+"\n"), fake.FS[K3sTokenPath])
	require.Equal(t, "pull-secret-value", registriesPullSecret(fake.FS[K3sRegistriesPath]))
}

func TestInstallServerJoinRefusesAgentToken(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	fake.FS["/root/token"] = []byte(encodeJoinToken("K10abc::node:secret", "pull", layout.RoleAgent) + "\n")
	_, err := Install(context.Background(), fake, InstallOptions{
		Role:         layout.RoleServer,
		Capabilities: []string{layout.CapabilityApplication},
		Join:         &JoinOptions{Server: "https://cp-1.internal:6443", TokenFile: "/root/token"},
	})
	require.ErrorContains(t, err, "minted for role agent, not server")
	require.Empty(t, fake.Writes, "a role mismatch must fail before any mutation")
}

func TestInstallAgentJoinRefusesServerToken(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	fake.FS["/root/token"] = []byte(encodeJoinToken("K10abc::server:secret", "pull", layout.RoleServer) + "\n")
	_, err := Install(context.Background(), fake, InstallOptions{
		Role:         layout.RoleAgent,
		Capabilities: []string{layout.CapabilityDatabase},
		Join:         &JoinOptions{Server: "https://cp-1.internal:6443", TokenFile: "/root/token"},
	})
	require.ErrorContains(t, err, "minted for role server, not agent")
	require.Empty(t, fake.Writes)
}

func TestInstallAgentEmptyTokenFile(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	fake.FS["/root/token"] = []byte("  \n")
	_, err := Install(context.Background(), fake, InstallOptions{
		Role:         layout.RoleAgent,
		Capabilities: []string{layout.CapabilityDatabase},
		Join:         &JoinOptions{Server: "https://cp-1.internal:6443", TokenFile: "/root/token"},
	})
	require.ErrorContains(t, err, "join token file /root/token is empty")
	require.Empty(t, fake.Writes, "token resolution must fail before any mutation")
}

func TestInstallRefusesUnknownCapability(t *testing.T) {
	t.Parallel()
	fake := linuxHost()
	_, err := Install(context.Background(), fake, InstallOptions{
		Capabilities: []string{"warp-drive"},
	})
	require.ErrorContains(t, err, `unknown capability "warp-drive"`)
	require.Empty(t, fake.Writes)
}

func TestInstallRefusesUnmanagedHost(t *testing.T) {
	t.Parallel()
	fake := withK3s(linuxHost(), "k3s.service", true)
	_, err := Install(context.Background(), fake, InstallOptions{
		Capabilities: []string{layout.CapabilityApplication},
	})
	require.ErrorContains(t, err, "will not be adopted or destroyed")
	require.Empty(t, fake.Writes, "the adoption guard must refuse before any mutation")
}

func TestInstallRefusesExistingInstallation(t *testing.T) {
	t.Parallel()
	fake := withRecord(t, withK3s(linuxHost(), "k3s.service", true), "server")
	_, err := Install(context.Background(), fake, InstallOptions{
		Capabilities: []string{layout.CapabilityApplication},
	})
	require.ErrorContains(t, err, "already carries a complete Skali installation")
	require.Empty(t, fake.Writes)
}

func TestInstallMatchingCompleteInstallationIsNoOp(t *testing.T) {
	t.Parallel()
	fake := withRecord(t, withK3s(linuxHost(), "k3s.service", true), "server")
	record, err := Install(context.Background(), fake, InstallOptions{
		Cluster:      "production",
		Role:         layout.RoleServer,
		Capabilities: []string{layout.CapabilityEdge, layout.CapabilityApplication},
	})
	require.NoError(t, err)
	require.Equal(t, "0f0f0f0f", record.InstallationID)
	require.Empty(t, fake.Writes)
}

func TestInstallCompleteNoOpStillRejectsConflictingTokenClaims(t *testing.T) {
	t.Parallel()
	fake := withRecord(t, withK3s(linuxHost(), "k3s.service", true), "server")
	record, err := LoadRecord(context.Background(), fake)
	require.NoError(t, err)
	record.Join = &JoinRecord{Server: "https://10.1.0.3:6443"}
	require.NoError(t, SaveRecord(context.Background(), fake, record))
	fake.Writes = nil

	token := encodeJoinTokenWithClaims("K10abc::server:secret", "", layout.RoleServer,
		"other", "https://10.1.0.3:6443")
	_, err = Install(context.Background(), fake, InstallOptions{
		Capabilities: []string{layout.CapabilityApplication, layout.CapabilityEdge},
		Join:         &JoinOptions{Token: token},
	})
	require.ErrorContains(t, err, "requested inputs do not match")
	require.Empty(t, fake.Writes)
}

func TestUninstallNodeRemovesRecordLast(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"/usr/local/bin/k3s-uninstall.sh": func(host.Command) (host.Result, error) {
			return host.Result{}, nil
		},
	}}
	fake.FS = map[string][]byte{k3sUninstallScript: []byte("script")}
	record := &Record{
		Version: RecordVersion, InstallationID: "x",
		Node: NodeRecord{Role: layout.RoleServer},
	}
	require.NoError(t, SaveRecord(ctx, fake, record))
	fake.Writes = nil

	require.NoError(t, UninstallNode(ctx, fake, record, nil, nil))
	require.Less(t, indexOf(fake.Writes, "remove "+CacheDir), indexOf(fake.Writes, "remove "+RecordPath))
	info, err := fake.Stat(ctx, StateDir)
	require.NoError(t, err)
	require.False(t, info.Exists)
}

func TestUninstallIncompletePreStartInstallUsesLocalCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &host.Fake{FS: map[string][]byte{
		K3sConfigPath:     []byte("staged"),
		K3sRegistriesPath: []byte("staged"),
	}}
	record := &Record{
		Version: RecordVersion, InstallationID: "x",
		Cluster: "e2e", Node: NodeRecord{Role: layout.RoleServer},
		Lifecycle: &InstallLifecycle{
			Status: InstallStatusFailed, Phase: InstallPhaseConfigured,
		},
	}
	require.NoError(t, SaveRecord(ctx, fake, record))
	require.NoError(t, UninstallNode(ctx, fake, record, nil, nil))
	info, err := fake.Stat(ctx, StateDir)
	require.NoError(t, err)
	require.False(t, info.Exists)
	_, present := fake.FS[K3sConfigPath]
	require.False(t, present)
	require.Empty(t, fake.Commands, "a never-started transaction needs no k3s command")
}

func TestUninstallReconstructsMissingUpstreamScript(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &host.Fake{}
	fake.Handlers = map[string]func(host.Command) (host.Result, error){
		"sh": func(host.Command) (host.Result, error) {
			fake.FS[k3sUninstallScript] = []byte("restored script")
			return host.Result{}, nil
		},
		k3sUninstallScript: func(host.Command) (host.Result, error) {
			return host.Result{}, nil
		},
	}
	record := &Record{
		Version: RecordVersion, InstallationID: "x",
		Cluster: "e2e", Node: NodeRecord{Role: layout.RoleServer},
		Lifecycle: &InstallLifecycle{
			Status: InstallStatusFailed, Phase: InstallPhaseStarting, StartAttempted: true,
		},
	}
	require.NoError(t, SaveRecord(ctx, fake, record))
	require.NoError(t, UninstallNode(ctx, fake, record, nil, nil))
	require.Len(t, fake.Commands, 2)
	require.Equal(t, "sh", fake.Commands[0].Name)
	require.Equal(t, k3sUninstallScript, fake.Commands[1].Name)
}

func TestUninstallLeavingServerRemovesSelfFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	nodePresent := true
	fake := &host.Fake{Handlers: map[string]func(host.Command) (host.Result, error){
		"k3s": func(cmd host.Command) (host.Result, error) {
			switch cmd.Args[1] {
			case "drain":
				return host.Result{ExitCode: 1, Stderr: "cannot delete pods\n"}, nil
			case "delete":
				nodePresent = false
				return host.Result{}, nil
			case "get":
				if nodePresent {
					return host.Result{}, nil
				}
				return host.Result{ExitCode: 1, Stderr: "not found\n"}, nil
			}
			return host.Result{ExitCode: 1}, nil
		},
		"/usr/local/bin/k3s-uninstall.sh": func(host.Command) (host.Result, error) {
			return host.Result{}, nil
		},
	}}
	fake.FS = map[string][]byte{k3sUninstallScript: []byte("script")}
	record := &Record{
		Version: RecordVersion, InstallationID: "x",
		Node: NodeRecord{Name: "cp-2", Role: layout.RoleServer},
	}
	require.NoError(t, SaveRecord(ctx, fake, record))
	fake.Writes = nil
	fake.Commands = nil

	plan := &NodeRemovalPlan{NodeName: "cp-2", Role: layout.RoleServer, Servers: 2, Total: 3}
	require.NoError(t, UninstallNode(ctx, fake, record, plan, nil))

	// Drain (tolerated failure), then the self node delete, then the
	// uninstall script: member removal needs a functional cluster, so the
	// delete must precede the script.
	require.Equal(t, "drain", fake.Commands[0].Args[1])
	require.Equal(t, "delete", fake.Commands[1].Args[1])
	scriptIndex := -1
	for i, cmd := range fake.Commands {
		if cmd.Name == "/usr/local/bin/k3s-uninstall.sh" {
			scriptIndex = i
		}
	}
	require.Greater(t, scriptIndex, 1)
}

func indexOf(entries []string, needle string) int {
	for index, entry := range entries {
		if entry == needle {
			return index
		}
	}
	return len(entries)
}
