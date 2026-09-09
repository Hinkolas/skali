package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/updates"
	"github.com/Hinkolas/skali/internal/version"
)

func confirmClusterUpdate(ctx context.Context, out io.Writer, reader *bufio.Reader, yes bool, target string) error {
	if yes {
		return nil
	}
	if !cliprompt.Interactive() {
		return errors.New("non-interactive upgrade requires --yes")
	}
	accepted, err := promptSession(out, reader).Confirm(ctx, cliprompt.ConfirmOptions{Title: "Update the whole cluster to " + target + "?"})
	if err != nil {
		return err
	}
	if !accepted {
		return errors.New("cluster update cancelled; nothing was changed")
	}
	return nil
}

func runManagedUpdate(ctx context.Context, out io.Writer, reader *bufio.Reader, api *client.Client, target string, yes, wait bool) error {
	status, err := api.UpdateStatus(ctx)
	if err != nil {
		return err
	}
	if status.Summary.State == "" {
		return errors.New("this daemon does not support coordinated CLI updates; install the new platform once using the previous CLI, or use explicit --recover --version on a controller")
	}
	if status.Summary.State == "updating" {
		if target != "" && target != status.Summary.TargetVersion {
			return fmt.Errorf("finish the running update to %s before requesting %s", status.Summary.TargetVersion, target)
		}
		if status.Operation == nil {
			return errors.New("update status has no operation; inspect update details")
		}
		fmt.Fprintf(out, "Cluster update to %s is already running.\n", status.Summary.TargetVersion)
		if wait {
			return waitAPIUpdate(ctx, out, api, status.Operation.ID)
		}
		return nil
	}
	if status.Summary.Action != "finish" && status.Summary.Action != "retry" && target == "" {
		status, err = api.ScanUpdates(ctx)
		if err != nil {
			return err
		}
	}
	if status.Summary.Action == "finish" || status.Summary.Action == "retry" {
		if target != "" && target != status.Summary.TargetVersion {
			return fmt.Errorf("finish the existing update to %s before requesting %s", status.Summary.TargetVersion, target)
		}
		target = status.Summary.TargetVersion
	}
	if target == "" {
		if status.Summary.Action == "update" {
			target = status.Summary.TargetVersion
		} else {
			fmt.Fprintln(out, status.Summary.State+": "+status.Summary.Detail)
			if status.Summary.State == "unknown" {
				return errors.New("update status could not be verified")
			}
			return nil
		}
	}
	if status.Summary.Action != "retry" {
		if err := updates.ValidateTarget(status, target); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Cluster: %s\nRelease: %s -> %s\nUpdates the platform and host services on all %d nodes, one node at a time.\n", api.Master(), status.Summary.ConvergedVersion, target, len(status.Nodes))
	if err := confirmClusterUpdate(ctx, out, reader, yes, target); err != nil {
		return err
	}
	retry := status.Summary.Action == "retry"
	err = withReauth(ctx, out, reader, api, func() error {
		var err error
		if retry {
			status, err = api.ResumeUpdate(ctx)
		} else {
			status, err = api.ApplyUpdate(ctx, target)
		}
		return err
	})
	if err != nil {
		return err
	}
	if status.Operation == nil {
		return errors.New("update accepted without an operation; inspect cluster status")
	}
	fmt.Fprintf(out, "Update accepted: %s. Work continues if this terminal disconnects.\n", status.Operation.ID)
	if wait {
		return waitAPIUpdate(ctx, out, api, status.Operation.ID)
	}
	return nil
}

func waitAPIUpdate(ctx context.Context, out io.Writer, api *client.Client, id string) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		status, err := api.UpdateStatus(ctx)
		if err != nil {
			var apiErr *client.APIError
			var identityErr *client.InstanceMismatchError
			if errors.As(err, &identityErr) || errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != 429 {
				return err
			}
			if last != "reconnecting" {
				fmt.Fprintln(out, "Reconnecting to the platform; the coordinator continues the update.")
				last = "reconnecting"
			}
			continue
		}
		if status.Operation == nil || status.Operation.ID != id {
			return errors.New("the operation is no longer the current update; inspect update details")
		}
		if status.Operation.Phase == "failed" {
			return fmt.Errorf("update failed: %s; run skali cluster upgrade to retry", status.Operation.Error)
		}
		if status.Operation.Phase == "complete" {
			if status.Summary.Action == "finish" || status.Summary.State == "unknown" {
				return fmt.Errorf("operation finished but cluster release is not verified: %s", status.Summary.Detail)
			}
			fmt.Fprintln(out, "Cluster updated to "+status.Operation.TargetVersion)
			return nil
		}
		if phase := status.Summary.Progress.Phase; phase != last {
			fmt.Fprintln(out, phase)
			last = phase
		}
	}
}

func runRecoveryUpdate(ctx context.Context, out io.Writer, reader *bufio.Reader, target string, yes, wait bool) error {
	if target == "" {
		return errors.New("--recover requires an exact --version")
	}
	store, _, err := reconciledClusterStore(ctx)
	if err != nil {
		return err
	}
	cluster := &updates.Cluster{Client: store.Client}
	status, err := cluster.RecoveryStatus(ctx)
	if err != nil {
		return err
	}
	if status.Operation != nil && !status.Operation.Settled() {
		if status.Operation.TargetVersion != target {
			return clusterstate.ErrOperationActive
		}
		if wait {
			return waitClusterOperation(ctx, store, status.Operation.ID)
		}
		fmt.Fprintln(out, "The requested update is already running.")
		return nil
	}
	if status.Summary.Action == "retry" {
		if target != status.Summary.TargetVersion {
			return fmt.Errorf("retry the existing update to %s before requesting %s", status.Summary.TargetVersion, target)
		}
		if !version.IsRelease(status.Installed.Version) || version.Older(target, status.Installed.Version) {
			return fmt.Errorf("cannot resume update to %s while the platform runs %s", target, status.Installed.Version)
		}
	} else {
		if err := updates.ValidateTarget(status, target); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Recovery update to %s on all %d nodes. Deployment and backup activity cannot be checked; ensure those operations are stopped before continuing.\n", target, len(status.Nodes))
	if err := confirmClusterUpdate(ctx, out, reader, yes, target); err != nil {
		return err
	}
	var op clusterstate.Operation
	_, err = store.Update(ctx, func(state *clusterstate.State) error {
		if state.CurrentOperation != "" {
			current := state.Operations[state.CurrentOperation]
			if !clusterstate.IsReleaseOperation(state, current) || state.Revisions[current.TargetRevision].Platform.Version != target || current.Phase != clusterstate.OperationFailed {
				return clusterstate.ErrOperationActive
			}
			resumed, err := clusterstate.ResumeRelease(state, time.Now())
			op = resumed
			return err
		}
		var err error
		op, err = clusterstate.RequestRelease(state, target, time.Now())
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Recovery update accepted: "+op.ID)
	if wait {
		return waitClusterOperation(ctx, store, op.ID)
	}
	return nil
}
