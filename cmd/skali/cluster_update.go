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
	"github.com/Hinkolas/skali/internal/platform"
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
	// Only a CLI at least as new as the target may move a cluster there:
	// the binary in PATH always manages every cluster it knows, so the
	// CLI upgrades first and the cluster follows.
	if home := homeRelease(); version.IsRelease(home) && version.Older(home, target) {
		return fmt.Errorf("cluster upgrade to %s needs a skali at least that new; this is skali %s. Run skali upgrade --version %s first, then skali cluster upgrade", target, home, target)
	}
	if status.Summary.Action != "retry" {
		if err := updates.ValidateTarget(status, target); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Cluster: %s\nRelease: %s -> %s\nUpdates the platform and host services on all %d nodes, one node at a time.\n", api.Master(), status.Summary.ConvergedVersion, target, len(status.Nodes))
	reportLegacyBuckets(ctx, out, api)
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

func waitAPIUpdate(ctx context.Context, out io.Writer, api *client.Client, id string) (result error) {
	defer func() {
		if result != nil {
			if _, dispatched := passthroughExit(result); !dispatched {
				result = fmt.Errorf("observation of cluster update %s ended: %w", id, result)
			}
		}
	}()
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
			if refusedAsWrongRelease(err) {
				return handoffUpdateObservation(ctx, api, id)
			}
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

// legacyBucket is a bucket still published on the installation-wide S3
// endpoint that releases before this CLI served; the target release
// publishes a bucket without a route in-cluster only.
type legacyBucket struct {
	Project     string
	Environment string
	Key         string
	Endpoint    string
}

// legacyBucketEndpoints finds the buckets whose published endpoint is
// neither a route of their own nor the in-cluster gateway: the ones that
// lose their public endpoint when the installation-wide S3 host goes
// away. The walk needs every project, which only an instance admin sees;
// anyone else gets an empty list and ok=false.
func legacyBucketEndpoints(ctx context.Context, api *client.Client) (found []legacyBucket, ok bool, err error) {
	session, err := api.CurrentSession(ctx)
	if err != nil {
		return nil, false, err
	}
	if session.User.Role != "admin" {
		return nil, false, nil
	}
	projects, err := api.ListProjects(ctx)
	if err != nil {
		return nil, false, err
	}
	internal := platform.InternalS3Endpoint()
	for _, project := range projects {
		environments, err := api.ListEnvironments(ctx, project.ID)
		if err != nil {
			return nil, false, err
		}
		for _, environment := range environments {
			status, err := api.EnvironmentStatus(ctx, environment.ID)
			if err != nil {
				return nil, false, err
			}
			for _, service := range status.Services {
				if service.Type != "bucket" || len(service.Routes) > 0 {
					continue
				}
				connection, err := api.BucketConnection(ctx, environment.ID, service.Key)
				if err != nil {
					return nil, false, err
				}
				if connection.Endpoint == "" || connection.Endpoint == internal {
					continue
				}
				found = append(found, legacyBucket{Project: project.Name, Environment: environment.Name,
					Key: service.Key, Endpoint: connection.Endpoint})
			}
		}
	}
	return found, true, nil
}

// reportLegacyBuckets prints what the update does to buckets still on the
// installation-wide S3 endpoint, so the operator can declare routes and
// deploy first. A failed check is reported and does not stop the update:
// the check is advice about the target release, not a precondition of it.
func reportLegacyBuckets(ctx context.Context, out io.Writer, api *client.Client) {
	found, ok, err := legacyBucketEndpoints(ctx, api)
	if err != nil {
		fmt.Fprintf(out, "note: could not check for buckets on the installation-wide S3 endpoint: %v\n", err)
		return
	}
	if !ok {
		fmt.Fprintln(out, "note: only an instance admin can check for buckets on the installation-wide S3 endpoint; buckets without a route become in-cluster only after this update")
		return
	}
	if len(found) == 0 {
		return
	}
	fmt.Fprintf(out, "This release removes the installation-wide S3 endpoint. %d bucket(s) still publish it and become reachable in-cluster only:\n", len(found))
	for _, bucket := range found {
		fmt.Fprintf(out, "  %s/%s  buckets.%s  %s\n", bucket.Project, bucket.Environment, bucket.Key, bucket.Endpoint)
	}
	fmt.Fprintln(out, "To keep a public hostname, declare route.domain on the bucket and deploy before updating; presigned URLs signed against the old host stop working either way.")
}
