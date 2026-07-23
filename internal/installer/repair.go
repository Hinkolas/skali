package installer

import (
	"context"
	"fmt"
	"strings"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/layout"
)

// RepairAction is one scoped, individually confirmed remediation. Every
// action is idempotent: re-running a completed repair is a no-op or a
// harmless re-application.
type RepairAction struct {
	// ID is stable for tests and logs.
	ID string
	// Title is the progress line; Confirm the individual question.
	Title   string
	Confirm string
	Run     func(ctx context.Context) error
}

// RepairDeps binds the planned actions to the host.
type RepairDeps struct {
	Runner host.Runner
	Record *Record
	// StampMissing marks an interrupted converge: the bundle version
	// matches this installer but the hash stamp is absent.
	StampMissing bool
	Progress     Progress
}

// PlanRepairs maps a diagnosis onto the v1 repair set, ordered host-level
// first: reinstall the k3s service (damaged unit or binary, readable
// record), restart k3s, heal registries.yaml, reconverge the bundle.
// Refusals name what repair deliberately does not do. Planning is pure;
// only the returned Run closures touch the host.
func PlanRepairs(diag *Diagnosis, deps RepairDeps) (actions []RepairAction, refusals []string) {
	progress := deps.Progress
	if progress == nil {
		progress = silentProgress{}
	}
	record := deps.Record

	if diag.Host.RecordRecovered && record != nil {
		actions = append(actions, RepairAction{
			ID:      "restore-record",
			Title:   "Restore the installation record",
			Confirm: "Atomically restore the primary installation record from its last valid backup. Continue? [y/N] ",
			Run: func(ctx context.Context) error {
				progress.Start("Restore " + RecordPath)
				if err := SaveRecord(ctx, deps.Runner, record); err != nil {
					return err
				}
				progress.Done("")
				return nil
			},
		})
	}

	if diag.Host.State == StateInterrupted && record != nil {
		actions = append(actions, RepairAction{
			ID:    "resume-install",
			Title: "Resume interrupted installation",
			Confirm: "Revalidate the recorded join endpoint and credential, then resume the k3s " +
				"installation from its durable transaction. Continue? [y/N] ",
			Run: func(ctx context.Context) error {
				opts := InstallOptions{
					Cluster:      record.Cluster,
					Role:         record.Node.Role,
					Capabilities: append([]string(nil), record.Node.Capabilities...),
					Endpoints:    record.Endpoints,
					TLS:          record.TLS,
					Progress:     progress,
				}
				if record.Join != nil {
					opts.Join = &JoinOptions{Server: record.Join.Server, TokenFile: K3sTokenPath}
				}
				_, err := Install(ctx, deps.Runner, opts)
				return err
			},
		})
		return actions, refusals
	}

	if diag.Host.State == StateDamaged && record == nil {
		refusals = append(refusals, "the installation record is unreadable; repair never guesses the "+
			"installation's identity. Restore the saved record; skali cluster restore lists the required inputs")
		return nil, refusals
	}

	failed := map[string]bool{}
	for _, check := range diag.Checks {
		if check.Severity == SeverityFail {
			failed[check.Name] = true
		}
	}
	role := layout.RoleServer
	if record != nil && record.Node.Role == layout.RoleAgent {
		role = layout.RoleAgent
	}

	needsK3sReinstall := diag.Host.State == StateDamaged && !diag.Host.RecordRecovered
	if diag.Host.State == StateDamaged && diag.Host.RecordRecovered {
		for _, problem := range diag.Host.Problems {
			if !strings.Contains(problem, RecordBackupPath) {
				needsK3sReinstall = true
				break
			}
		}
	}
	if needsK3sReinstall {
		actions = append(actions, RepairAction{
			ID:    "reinstall-k3s",
			Title: "Reinstall the k3s service",
			Confirm: "Re-run the k3s install script under the recorded role and pin. The k3s " +
				"configuration, the registry mirror, and the datastore are preserved. Continue? [y/N] ",
			Run: func(ctx context.Context) error {
				if err := upgradeK3s(ctx, deps.Runner, role, progress); err != nil {
					return err
				}
				return waitK3sUpgraded(ctx, deps.Runner, role, progress)
			},
		})
	} else if failed["k3s service"] || failed["kubernetes api"] {
		unit := "k3s"
		if role == layout.RoleAgent {
			unit = "k3s-agent"
		}
		actions = append(actions, RepairAction{
			ID:    "restart-k3s",
			Title: "Restart " + unit,
			Confirm: "Restart the " + unit + " service. Workload containers keep running " +
				"through the restart. Continue? [y/N] ",
			Run: func(ctx context.Context) error {
				progress.Start("Restart " + unit)
				result, err := deps.Runner.Run(ctx, host.Command{
					Name: "systemctl", Args: []string{"restart", unit},
				})
				if err != nil {
					return err
				}
				if result.ExitCode != 0 {
					return fmt.Errorf("systemctl restart %s: exit %d: %s", unit, result.ExitCode,
						strings.TrimSpace(result.Stderr))
				}
				return waitK3sUpgraded(ctx, deps.Runner, role, progress)
			},
		})
	}

	needsConverge := deps.StampMissing
	if failed["registry mirror"] {
		if role == layout.RoleServer {
			actions = append(actions, RepairAction{
				ID:    "heal-registries",
				Title: "Rewrite " + K3sRegistriesPath,
				Confirm: "Mint a new node pull credential and rewrite " + K3sRegistriesPath + " as a " +
					"fresh install would; k3s restarts to load it (containers keep running). The " +
					"following reconverge publishes the credential in-cluster. Continue? [y/N] ",
				Run: func(ctx context.Context) error {
					return HealNodePullCredential(ctx, deps.Runner, true, progress)
				},
			})
			needsConverge = true
		} else {
			refusals = append(refusals, "this agent's registries.yaml is missing its pull credential; "+
				"healing would mint a credential the cluster does not know. Re-join the node with a "+
				"fresh token from skali cluster token")
		}
	}

	if role == layout.RoleServer &&
		(needsConverge || failed["bootstrap database"] || failed["managed registry"] || failed["skalid"]) {
		actions = append(actions, RepairAction{
			ID:    "reconverge",
			Title: "Reconverge the bundle",
			Confirm: "Re-apply the bundle from the running cluster's own inputs (no version change, " +
				"no prompts) and wait for health. Continue? [y/N] ",
			Run: func(ctx context.Context) error {
				return repairReconverge(ctx, deps.Runner, record, progress)
			},
		})
	}
	return actions, refusals
}

// repairReconverge re-applies the bundle from LiveProfile: the same
// converge an upgrade runs, minus any version movement. The init-owner
// rule holds here exactly as in Init: a secondary server must not churn
// the published record and hash.
func repairReconverge(ctx context.Context, runner host.Runner, record *Record, progress Progress) error {
	client, err := KubeClient(ctx, runner)
	if err != nil {
		return err
	}
	if published, err := InClusterRecord(ctx, client); err == nil && published != nil &&
		published.Node.Name != "" && published.Node.Name != record.Node.Name {
		return fmt.Errorf("the bundle is maintained on %s; run skali cluster repair there", published.Node.Name)
	}
	profile, _, err := LiveProfile(ctx, client, runner, record)
	if err != nil {
		return err
	}
	if err := bundle.Converge(ctx, client, profile, progress); err != nil {
		return err
	}
	progress.Start("Wait for skalid ready")
	if err := waitSkalidHealthy(ctx, client); err != nil {
		return err
	}
	progress.Done("")
	return bundle.StampHash(ctx, client, profile)
}
