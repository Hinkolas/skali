package hostdaemon

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/version"
)

type Agent struct {
	ConfigPath string
	Logger     *slog.Logger
	Runner     host.Runner
	// Downloads fetches release assets for upgrade actions; nil uses a
	// client with a generous timeout suited to a binary download.
	Downloads *http.Client
}

// errRestarting marks an upgrade action that replaced this binary: the
// process is about to be restarted by systemd, so nothing is reported and
// the new binary picks the same action up on its first poll.
var errRestarting = errors.New("hostd is restarting into the new release")

func (a *Agent) Run(ctx context.Context) error {
	if a.ConfigPath == "" {
		a.ConfigPath = installer.AgentConfigPath
	}
	if a.Runner == nil {
		a.Runner = host.Local{}
	}
	config, client, err := a.load()
	if err != nil {
		return err
	}
	pending := a.report()
	nextRenewalCheck := time.Time{}
	for {
		if time.Now().After(nextRenewalCheck) {
			config, client, err = a.renewIfNeeded(ctx, config, client)
			if err != nil {
				a.log("agent certificate renewal check failed", "error", err)
			}
			nextRenewalCheck = time.Now().Add(time.Hour)
		}
		response, endpoint, err := a.poll(ctx, client, config, pending)
		if err != nil {
			a.log("agent poll failed", "error", err)
			if !waitContext(ctx, 5*time.Second) {
				return ctx.Err()
			}
			continue
		}
		discovered := response.Coordinators
		if len(discovered) == 0 {
			discovered = config.Endpoints
		}
		if endpoint != "" {
			discovered = preferEndpoint(discovered, endpoint)
		}
		if !slices.Equal(config.Endpoints, discovered) {
			if err := installer.UpdateAgentEndpoints(ctx, a.Runner, config, discovered); err != nil {
				a.log("persist coordinator discovery failed", "error", err)
			} else {
				config.Endpoints = append([]string(nil), discovered...)
			}
		}
		if err := installer.CacheCoordinatorState(ctx, a.Runner, response); err != nil {
			a.log("cache coordinator state failed", "error", err)
		}
		pending = a.report()
		if response.Action.ID != "" {
			report, restarting := a.execute(ctx, response.Action)
			if restarting {
				// The binary was swapped and a restart is scheduled; wait
				// for it rather than reporting a step this process cannot
				// finish. The successor completes it under the same attempt.
				<-ctx.Done()
				return ctx.Err()
			}
			pending = report
			// Acknowledge typed actions immediately. The final cleanup
			// action schedules this daemon's own removal seconds later.
			continue
		}
		delay := response.RetryAfter
		if delay <= 0 || delay > time.Minute {
			delay = time.Duration(config.PollSeconds) * time.Second
		}
		if !waitContext(ctx, delay) {
			return ctx.Err()
		}
	}
}

func (a *Agent) renewIfNeeded(ctx context.Context, config installer.AgentConfig,
	client *http.Client) (installer.AgentConfig, *http.Client, error) {
	certificatePEM, err := os.ReadFile(config.ClientCert)
	if err != nil {
		return config, client, err
	}
	block, _ := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return config, client, errors.New("agent certificate is malformed")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return config, client, err
	}
	if time.Until(certificate.NotAfter) > 48*time.Hour {
		return config, client, nil
	}
	if config.NodeName == "" {
		return config, client, errors.New("agent config is missing the node name required for renewal")
	}
	privateKey, csr, err := clusterstate.NewAgentKeyAndCSR(config.NodeID, config.NodeName)
	if err != nil {
		return config, client, err
	}
	body, err := json.Marshal(clusterstate.RenewRequest{CSR: string(csr)})
	if err != nil {
		return config, client, err
	}
	var lastErr error
	for _, endpoint := range config.Endpoints {
		endpoint, err = clusterstate.NormalizeEndpoint(endpoint)
		if err != nil {
			lastErr = err
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimSuffix(endpoint, "/")+"/v1/agent/renew", bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if response.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("coordinator returned HTTP %d during certificate renewal",
				response.StatusCode)
			continue
		}
		var renewed clusterstate.RenewResponse
		if err := json.Unmarshal(data, &renewed); err != nil {
			lastErr = err
			continue
		}
		ca, err := os.ReadFile(config.CACert)
		if err != nil {
			return config, client, err
		}
		if err := installer.SaveAgentIdentity(ctx, a.Runner, config, ca,
			[]byte(renewed.Certificate), privateKey); err != nil {
			return config, client, err
		}
		return a.load()
	}
	if lastErr == nil {
		lastErr = errors.New("agent has no usable coordinator endpoint")
	}
	return config, client, lastErr
}

func (a *Agent) load() (installer.AgentConfig, *http.Client, error) {
	data, err := os.ReadFile(a.ConfigPath)
	if err != nil {
		return installer.AgentConfig{}, nil, fmt.Errorf("read agent config: %w", err)
	}
	var config installer.AgentConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return config, nil, fmt.Errorf("parse agent config: %w", err)
	}
	if config.NodeID == "" || len(config.Endpoints) == 0 {
		return config, nil, errors.New("agent config is missing identity or coordinator endpoints")
	}
	ca, err := os.ReadFile(config.CACert)
	if err != nil {
		return config, nil, err
	}
	cert, err := os.ReadFile(config.ClientCert)
	if err != nil {
		return config, nil, err
	}
	key, err := os.ReadFile(config.ClientKey)
	if err != nil {
		return config, nil, err
	}
	tlsConfig, err := clusterstate.AgentTLSConfig(ca, cert, key)
	if err != nil {
		return config, nil, err
	}
	return config, &http.Client{
		Timeout:   20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
	}, nil
}

func (a *Agent) poll(ctx context.Context, client *http.Client, config installer.AgentConfig,
	report clusterstate.AgentReport) (clusterstate.AgentPollResponse, string, error) {
	var lastErr error
	for _, endpoint := range config.Endpoints {
		endpoint, err := clusterstate.NormalizeEndpoint(endpoint)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := json.Marshal(clusterstate.AgentPollRequest{Report: report})
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimSuffix(endpoint, "/")+"/v1/agent/poll", bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if response.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("coordinator %s returned HTTP %d", endpoint, response.StatusCode)
			continue
		}
		var result clusterstate.AgentPollResponse
		if err := json.Unmarshal(data, &result); err != nil {
			lastErr = err
			continue
		}
		return result, endpoint, nil
	}
	if lastErr == nil {
		lastErr = errors.New("agent has no usable coordinator endpoint")
	}
	return clusterstate.AgentPollResponse{}, "", lastErr
}

func (a *Agent) execute(ctx context.Context, action clusterstate.AgentAction) (clusterstate.AgentReport, bool) {
	report := clusterstate.AgentReport{
		ActionID: action.ID, Phase: clusterstate.NodePhaseFailed, AgentVersion: version.Version,
	}
	redactor := redact.New(map[string]string{
		action.K3sToken: "k3s-token", action.PullSecret: "registry-pull-secret",
	})
	var err error
	switch action.Type {
	case clusterstate.NodeActionUpgrade:
		var record *installer.Record
		record, err = a.upgrade(ctx, action)
		if errors.Is(err, errRestarting) {
			return clusterstate.AgentReport{}, true
		}
		if err == nil {
			report.K3sVersion = record.Versions.K3s
		}
	case clusterstate.NodeActionInstall:
		_, err = installer.Install(ctx, a.Runner, installer.InstallOptions{
			Cluster: action.Cluster, Role: action.Role,
			Capabilities: append([]string(nil), action.Capabilities...),
			// Only the cluster address travels in the action; the rest of
			// this host's address declaration is read back from its own
			// enrollment record inside Install.
			Network:    installer.NodeNetwork{ClusterIP: action.NodeIP},
			Management: installer.ManagementReconciled,
			Pending:    true,
			Join: &installer.JoinOptions{
				Server: action.Server, Token: action.K3sToken,
				PullSecret: action.PullSecret,
			},
		})
		if err == nil && action.Role == "server" {
			err = installer.StartHostd(ctx, a.Runner, true)
		}
	case clusterstate.NodeActionCapabilities:
		var record *installer.Record
		record, err = installer.LoadRecord(ctx, a.Runner)
		if err == nil {
			err = installer.PersistNodeCapabilities(ctx, a.Runner, record, action.Capabilities)
		}
	case clusterstate.NodeActionRemove:
		var record *installer.Record
		record, err = installer.LoadRecord(ctx, a.Runner)
		if err == nil {
			err = installer.DecommissionK3s(ctx, a.Runner, record)
		}
	case "cleanup":
		err = installer.ScheduleHostdSelfRemoval(ctx, a.Runner)
	default:
		err = fmt.Errorf("coordinator requested unknown action %q", action.Type)
	}
	if err != nil {
		report.Error = redactor.Redact(err.Error())
		return report, false
	}
	report.ActionOK = true
	report.Phase = clusterstate.NodePhaseActive
	if action.Type == clusterstate.NodeActionRemove {
		report.Phase = clusterstate.NodePhaseUninstalling
	} else if action.Type == "cleanup" {
		report.Phase = clusterstate.NodePhaseRemoved
	}
	return report, false
}

// upgrade moves this host to the release an action names, in two passes
// keyed on the running binary so the step is idempotent across the restart
// it causes. The old binary downloads the new hostd, verifies it against the
// checksum the coordinator read from the release, swaps it in, and schedules
// the restart (errRestarting). The new binary, handed the same still-running
// step on its first poll, moves k3s to its own compiled-in pin when the host
// drifted and records the versions; a k3s move the pin does not support
// fails the step with the same wording the CLI uses.
func (a *Agent) upgrade(ctx context.Context, action clusterstate.AgentAction) (*installer.Record, error) {
	if action.Version == "" {
		return nil, errors.New("upgrade action names no version")
	}
	record, err := installer.LoadRecord(ctx, a.Runner)
	if err != nil {
		return nil, err
	}
	if version.Version != action.Version {
		client := a.Downloads
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Minute}
		}
		binary, err := installer.DownloadHostd(ctx, client, installer.ReleaseBase,
			action.Version, runtime.GOARCH, action.HostdSHA256)
		if err != nil {
			return nil, err
		}
		if record.Coordinator != nil {
			record.Coordinator.AgentVersion = action.Version
			if err := installer.SaveRecord(ctx, a.Runner, record); err != nil {
				return nil, err
			}
		}
		if err := installer.ReplaceHostdAndRestart(ctx, a.Runner, binary,
			record.Node.Role == layout.RoleServer); err != nil {
			return nil, err
		}
		a.log("hostd replaced; restarting into the new release", "version", action.Version)
		return nil, errRestarting
	}
	installed := installer.ProbeK3sVersion(ctx, a.Runner)
	if installed != installer.K3sVersion {
		if err := installer.CheckK3sMove(installed, installer.K3sVersion); err != nil {
			return nil, err
		}
		if err := installer.UpgradeK3s(ctx, a.Runner, record, silentProgress{}); err != nil {
			return nil, err
		}
	}
	record.Versions.K3s = installer.K3sVersion
	record.Versions.Installer = version.Version
	if record.Coordinator != nil {
		record.Coordinator.AgentVersion = version.Version
	}
	if err := installer.SaveRecord(ctx, a.Runner, record); err != nil {
		return nil, err
	}
	return record, nil
}

type silentProgress struct{}

func (silentProgress) Start(string) {}
func (silentProgress) Done(string)  {}
func (silentProgress) Skip(string)  {}
func (silentProgress) Note(string)  {}

func (a *Agent) report() clusterstate.AgentReport {
	record, err := installer.LoadRecord(context.Background(), a.Runner)
	if err != nil {
		return clusterstate.AgentReport{
			Phase: clusterstate.NodePhaseFailed, Error: err.Error(), AgentVersion: version.Version,
		}
	}
	report := clusterstate.AgentReport{Phase: clusterstate.NodePhaseActive, AgentVersion: version.Version}
	if record.Lifecycle != nil && record.Lifecycle.Phase != "" {
		switch record.Lifecycle.Phase {
		case installer.InstallPhaseEnrolled, installer.InstallPhaseAwaitingApply:
			report.Phase = clusterstate.NodePhaseAwaitingApply
		case installer.InstallPhasePrepared, installer.InstallPhaseConfigured,
			installer.InstallPhaseInstalled:
			report.Phase = clusterstate.NodePhaseInstalling
		case installer.InstallPhaseStarting:
			report.Phase = clusterstate.NodePhaseStarting
		case installer.InstallPhaseJoined:
			report.Phase = clusterstate.NodePhaseJoined
		case installer.InstallPhaseDraining:
			report.Phase = clusterstate.NodePhaseDraining
		case installer.InstallPhaseUninstalling:
			report.Phase = clusterstate.NodePhaseUninstalling
		case installer.InstallPhaseRemoved:
			report.Phase = clusterstate.NodePhaseRemoved
		case installer.InstallPhaseComplete, installer.InstallPhaseActive:
			report.Phase = clusterstate.NodePhaseActive
		default:
			report.Phase = record.Lifecycle.Phase
		}
		if record.Lifecycle.Status == installer.InstallStatusFailed {
			report.Phase = clusterstate.NodePhaseFailed
			report.Error = record.Lifecycle.LastError
		}
	}
	report.K3sVersion = record.Versions.K3s
	return report
}

func (a *Agent) log(message string, args ...any) {
	if a.Logger != nil {
		a.Logger.Warn(message, args...)
	}
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func preferEndpoint(endpoints []string, preferred string) []string {
	result := []string{preferred}
	for _, endpoint := range endpoints {
		if endpoint != preferred {
			result = append(result, endpoint)
		}
	}
	return result
}
