package updates

import (
	"fmt"
	"time"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/version"
)

type Progress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Percent int    `json:"percent"`
	Phase   string `json:"phase"`
}

type Summary struct {
	State            string   `json:"state"`
	ConvergedVersion string   `json:"converged_version,omitempty"`
	TargetVersion    string   `json:"target_version,omitempty"`
	Action           string   `json:"action"`
	Detail           string   `json:"detail,omitempty"`
	Progress         Progress `json:"progress"`
}

// summarize is shared by status, apply eligibility and automatic updates.
// It never equates the product daemon's version with cluster convergence.
func summarize(status *Status, expectedK3s string, now time.Time) Summary {
	s := Summary{State: "unknown", ConvergedVersion: status.Installed.PlatformVersion, Progress: Progress{Phase: "Preparing"}}
	if !status.Managed {
		s.Detail = status.Reason
		return s
	}
	if op := status.Operation; op != nil && (!op.Settled() || op.Phase == "failed") {
		s.TargetVersion = op.TargetVersion
		s.Progress.Total = len(op.Steps) + 2
		for _, step := range op.Steps {
			if step.Phase == "complete" {
				s.Progress.Done++
			}
		}
		switch op.Phase {
		case "upgrading-nodes", "activating-topology":
			s.Progress.Phase = "Updating nodes"
		case "reconciling-platform":
			s.Progress.Phase = "Updating platform"
		case "verifying":
			s.Progress.Phase = "Verifying"
			s.Progress.Done++
		}
		s.Progress.Percent = 100 * s.Progress.Done / s.Progress.Total
		s.State = "updating"
		if op.Phase == "failed" {
			s.State, s.Action, s.Detail = "failed", "retry", op.Error
		}
		return s
	}
	versions := []string{status.Installed.Version, status.Installed.PlatformVersion}
	for _, node := range status.Nodes {
		if node.K3sVersion == "" {
			s.Detail = fmt.Sprintf("Node %s has not reported its Kubernetes version.", node.Name)
			return s
		}
		versions = append(versions, node.AgentVersion)
		if node.CoordinatorVersion != "" {
			versions = append(versions, node.CoordinatorVersion)
		}
	}
	for _, installed := range versions {
		if !version.IsRelease(installed) {
			s.Detail = fmt.Sprintf("Cannot determine cluster release: component version %q is unknown or a development build.", installed)
			return s
		}
		if s.TargetVersion == "" || version.Older(s.TargetVersion, installed) {
			s.TargetVersion = installed
		}
	}
	partial := false
	for _, installed := range versions {
		partial = partial || installed != s.TargetVersion
	}
	for _, node := range status.Nodes {
		partial = partial || expectedK3s != "" && node.K3sVersion != expectedK3s
		if node.Role == "server" {
			partial = partial || node.CoordinatorVersion != s.TargetVersion
		}
	}
	if partial {
		s.State, s.Action = "incomplete", "finish"
		s.Detail = "Some components have not reached the same release. Finish the update to bring the cluster into agreement."
		return s
	}
	for _, node := range status.Nodes {
		if node.Phase != "active" || node.LastSeen.IsZero() || now.Sub(node.LastSeen) > clusterstate.HeartbeatWindow ||
			node.Role == "server" && (node.CoordinatorLastSeen.IsZero() || now.Sub(node.CoordinatorLastSeen) > clusterstate.HeartbeatWindow) {
			s.Detail = "Waiting for fresh, healthy reports from every node."
			return s
		}
	}
	if len(status.Nodes) == 0 {
		s.Detail = "No active cluster membership is available."
		return s
	}
	if status.Latest != nil && version.Older(s.TargetVersion, status.Latest.Version) {
		s.State, s.Action, s.TargetVersion = "available", "update", status.Latest.Version
		return s
	}
	if status.LastError != "" {
		s.Detail = "Could not check for a newer release."
		return s
	}
	if status.LastCheckedAt == nil {
		s.State = "not_checked"
		return s
	}
	if status.Latest == nil {
		s.State, s.Detail = "no_release", "No releases are available on this channel."
		return s
	}
	s.State = "current"
	return s
}

// ValidateTarget also guards exact-version requests that bypass feed selection.
func ValidateTarget(status *Status, target string) error {
	if !version.IsRelease(target) {
		return &BlockedError{Reason: fmt.Sprintf("%q is not a tagged release", target)}
	}
	if status.Summary.Action == "retry" || status.Summary.State == "updating" {
		return clusterstate.ErrOperationActive
	}
	if !status.Managed && version.IsRelease(status.Installed.Version) && !version.Older(status.Installed.Version, target) {
		return ErrNotNewer
	}
	if !status.Managed || !status.Manageable {
		return &BlockedError{Reason: status.Reason}
	}
	if status.Summary.TargetVersion == "" {
		return &BlockedError{Reason: status.Summary.Detail}
	}
	versions := []string{status.Installed.Version, status.Installed.PlatformVersion}
	for _, node := range status.Nodes {
		versions = append(versions, node.AgentVersion)
		if node.CoordinatorVersion != "" {
			versions = append(versions, node.CoordinatorVersion)
		}
	}
	newer := false
	for _, installed := range versions {
		if !version.IsRelease(installed) || version.Older(target, installed) {
			return &BlockedError{Reason: fmt.Sprintf("cannot move component running %q to %s", installed, target)}
		}
		newer = newer || version.Older(installed, target)
	}
	if !newer && status.Summary.Action != "finish" {
		return ErrNotNewer
	}
	return nil
}
