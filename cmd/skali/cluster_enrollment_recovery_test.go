package main

import (
	"context"
	"errors"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

type enrollmentFaultHost struct {
	*host.Fake
	failPath  string
	failStart bool
}

func (h *enrollmentFaultHost) ReplaceFile(ctx context.Context, path, backup string, data []byte, mode fs.FileMode) error {
	if path == h.failPath {
		return errors.New("injected write failure")
	}
	return h.Fake.ReplaceFile(ctx, path, backup, data, mode)
}
func (h *enrollmentFaultHost) Run(ctx context.Context, cmd host.Command) (host.Result, error) {
	if h.failStart && cmd.Name == "systemctl" && len(cmd.Args) > 0 && cmd.Args[0] == "enable" {
		return host.Result{}, errors.New("injected startup failure")
	}
	return h.Fake.Run(ctx, cmd)
}

func enrollmentHost() *enrollmentFaultHost {
	return &enrollmentFaultHost{Fake: &host.Fake{
		FS: map[string][]byte{"/etc/os-release": []byte("NAME=Ubuntu\nPRETTY_NAME=Ubuntu\n"), "/run/systemd/system": nil},
		Handlers: map[string]func(host.Command) (host.Result, error){
			"hostname": func(host.Command) (host.Result, error) { return host.Result{Stdout: "db-01\n"}, nil },
			"uname": func(cmd host.Command) (host.Result, error) {
				if cmd.Args[0] == "-s" {
					return host.Result{Stdout: "Linux\n"}, nil
				}
				return host.Result{Stdout: "aarch64\n"}, nil
			},
			"id": func(host.Command) (host.Result, error) { return host.Result{Stdout: "0\n"}, nil },
			"systemctl": func(cmd host.Command) (host.Result, error) {
				if len(cmd.Args) > 1 && (cmd.Args[1] == "k3s.service" || cmd.Args[1] == "k3s-agent.service") {
					return host.Result{ExitCode: 4, Stdout: "not-found\n"}, nil
				}
				return host.Result{Stdout: "active\n"}, nil
			},
		},
	}}
}

func cliEnrollmentServer(t *testing.T) (*clusterstate.Store, *httptest.Server, string) {
	t.Helper()
	store := &clusterstate.Store{Client: fake.NewSimpleClientset()}
	state, err := clusterstate.NewSeedState("kilohertz", clusterstate.Node{ID: uuid.NewString(), InstallationID: uuid.NewString(), Name: "ctrl-01", Role: layout.RoleServer, Capabilities: append([]string(nil), layout.Capabilities...)}, time.Now())
	require.NoError(t, err)
	_, err = store.Bootstrap(context.Background(), state)
	require.NoError(t, err)
	_, token, err := store.CreateInvitation(context.Background(), layout.RoleAgent, []string{layout.CapabilityDatabase}, time.Hour)
	require.NoError(t, err)
	coordinator := &clusterstate.Coordinator{Store: store}
	server := httptest.NewUnstartedServer(coordinator.Handler())
	server.TLS, err = coordinator.TLSConfig(context.Background())
	require.NoError(t, err)
	server.StartTLS()
	t.Cleanup(server.Close)
	token, err = clusterstate.WithCoordinators(token, []string{server.URL})
	require.NoError(t, err)
	return store, server, token
}

func TestEnrollmentResumesEachLocalFailureWithoutRepeatingInputs(t *testing.T) {
	for _, point := range []string{installer.AgentPendingTokenPath, installer.AgentPendingKeyPath, installer.AgentPendingCSRPath, installer.HostdAgentUnitPath, installer.AgentCACertPath, installer.AgentClientCertPath, installer.AgentClientKeyPath, installer.AgentConfigPath, "start"} {
		t.Run(point, func(t *testing.T) {
			store, _, token := cliEnrollmentServer(t)
			target := enrollmentHost()
			if point == "start" {
				target.failStart = true
			} else {
				target.failPath = point
			}
			_, err := runReconciledEnrollment(context.Background(), reconciledEnrollmentOptions{Token: token, Network: installer.NodeNetwork{ClusterIP: "10.10.1.10"}, Runner: target, HostdBinary: []byte("hostd")})
			require.Error(t, err)
			require.Contains(t, err.Error(), "sudo skali cluster join")
			require.NotContains(t, err.Error(), token)
			saved, err := installer.LoadRecord(context.Background(), target)
			require.NoError(t, err)
			require.Equal(t, installer.InstallStatusFailed, saved.Lifecycle.Status)
			target.failPath = ""
			target.failStart = false
			opts := reconciledEnrollmentOptions{Runner: target, HostdBinary: []byte("hostd")}
			// If the token itself could not be written, only that input is needed again.
			if point == installer.AgentPendingTokenPath {
				opts.Token = token
			}
			joined, err := runReconciledEnrollment(context.Background(), opts)
			require.NoError(t, err)
			require.Equal(t, saved.Node.ID, joined.Node.ID)
			require.Equal(t, saved.InstallationID, joined.InstallationID)
			require.Equal(t, installer.InstallPhaseAwaitingApply, joined.Lifecycle.Phase)
			state, err := store.Load(context.Background())
			require.NoError(t, err)
			require.Len(t, state.Nodes, 2)
			require.Len(t, state.Revisions, 2)
			require.NotContains(t, target.FS, installer.AgentPendingTokenPath)
			require.Equal(t, fs.FileMode(0600), target.Modes[installer.AgentClientKeyPath])
			for _, command := range target.Commands {
				require.NotEqual(t, "k3s", command.Name, "enrollment must not install or invoke k3s")
			}
		})
	}
}

func TestStartupResumeDoesNotContactExpiredInvitation(t *testing.T) {
	store, server, token := cliEnrollmentServer(t)
	target := enrollmentHost()
	target.failStart = true
	_, err := runReconciledEnrollment(context.Background(), reconciledEnrollmentOptions{Token: token, Network: installer.NodeNetwork{ClusterIP: "10.10.1.10"}, Runner: target, HostdBinary: []byte("hostd")})
	require.Error(t, err)
	parsed, err := clusterstate.ParseToken(token)
	require.NoError(t, err)
	require.NoError(t, store.RevokeInvitation(context.Background(), parsed.Invitation))
	server.Close() // Local startup recovery must work without any coordinator.
	target.failStart = false
	record, err := runReconciledEnrollment(context.Background(), reconciledEnrollmentOptions{Runner: target, HostdBinary: []byte("hostd")})
	require.NoError(t, err)
	require.Equal(t, installer.InstallPhaseAwaitingApply, record.Lifecycle.Phase)
}

func TestAlpha2PendingRecordAcceptsReplacementInvitation(t *testing.T) {
	store, _, token := cliEnrollmentServer(t)
	target := enrollmentHost()
	target.failPath = installer.AgentConfigPath
	_, err := runReconciledEnrollment(context.Background(), reconciledEnrollmentOptions{Token: token, Network: installer.NodeNetwork{ClusterIP: "10.10.1.10"}, Runner: target, HostdBinary: []byte("hostd")})
	require.Error(t, err)
	saved, err := installer.LoadRecord(context.Background(), target)
	require.NoError(t, err)
	key := append([]byte(nil), target.FS[installer.AgentPendingKeyPath]...)
	require.NoError(t, target.Remove(context.Background(), installer.AgentPendingTokenPath)) // alpha.2 did not retain it.
	parsed, err := clusterstate.ParseToken(token)
	require.NoError(t, err)
	require.NoError(t, store.RevokeInvitation(context.Background(), parsed.Invitation))
	_, replacement, err := store.CreateInvitation(context.Background(), layout.RoleAgent, []string{layout.CapabilityDatabase}, time.Hour)
	require.NoError(t, err)
	target.failPath = ""
	joined, err := runReconciledEnrollment(context.Background(), reconciledEnrollmentOptions{Token: replacement, Runner: target, HostdBinary: []byte("hostd")})
	require.NoError(t, err)
	require.Equal(t, saved.Node.ID, joined.Node.ID)
	require.Equal(t, key, target.FS[installer.AgentClientKeyPath])
	state, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, state.Revisions, 2)
}

func TestExplicitJoinInputsRejectOtherTrustBeforeRequests(t *testing.T) {
	_, _, token := cliEnrollmentServer(t)
	target := enrollmentHost()
	target.failStart = true
	_, err := runReconciledEnrollment(context.Background(), reconciledEnrollmentOptions{Token: token, Network: installer.NodeNetwork{ClusterIP: "10.10.1.10"}, Runner: target, HostdBinary: []byte("hostd")})
	require.Error(t, err)
	other, _, err := clusterstate.NewToken("different", "sha256:"+strings.Repeat("0", 64))
	require.NoError(t, err)
	_, err = runReconciledEnrollment(context.Background(), reconciledEnrollmentOptions{Token: other, Runner: target, HostdBinary: []byte("hostd")})
	require.ErrorContains(t, err, "different coordinator trust")
}
