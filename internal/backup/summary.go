package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/Hinkolas/skali/internal/store"
)

// TargetSummary is the backup target at a glance: whether it answers, what
// the snapshots in it hold, and how backups have been running. The
// snapshot part comes from the bucket and is nil while it does not answer;
// the activity comes from the control-plane database.
type TargetSummary struct {
	Reachable bool
	// UnreachableReason says why the bucket did not answer.
	UnreachableReason string
	Snapshots         *SnapshotTotals
	Runs              RunTotals
	Schedules         ScheduleTotals
	CheckedAt         time.Time
}

// SnapshotTotals counts the snapshots whose manifests the bucket holds.
// Bytes is what their components hold; objects no manifest references
// (an interrupted backup's leftovers) are not counted.
type SnapshotTotals struct {
	Count     int
	Bytes     int64
	Manual    int
	Scheduled int
	// Orphaned snapshots belong to environments the control plane no
	// longer has.
	Orphaned int
	OldestAt *time.Time
	NewestAt *time.Time
	// Projects is the split by project, largest first.
	Projects []ProjectSnapshotTotals
}

type ProjectSnapshotTotals struct {
	Project  string
	Count    int
	Bytes    int64
	NewestAt time.Time
}

// RunTotals is the backup activity the database records.
type RunTotals struct {
	// Since bounds Succeeded and Failed.
	Since     time.Time
	Succeeded int64
	Failed    int64
	// BackingUp and Restoring are queued or under way right now.
	BackingUp     int64
	Restoring     int64
	LastSucceeded *RunInfo
	LastFailed    *RunInfo
}

type RunInfo struct {
	Project     string
	Environment string
	FinishedAt  time.Time
	Error       string
}

// ScheduleTotals is how many environments back up on a schedule.
type ScheduleTotals struct {
	Environments int64
	Scheduled    int64
}

// summaryWindow bounds the succeeded and failed run counts.
const summaryWindow = 7 * 24 * time.Hour

// summaryConcurrency is how many environment snapshot listings run at once.
const summaryConcurrency = 8

// manifestCache remembers what each manifest of the target says. A
// manifest is written once, last, and never changed, so a summary lists
// the snapshot directories and only reads manifests it has not seen. The
// cache holds the newest scan of one target; entries a scan no longer
// finds (deleted or expired snapshots) are dropped with it.
type manifestCache struct {
	mu        sync.Mutex
	target    string
	summaries map[string]SnapshotSummary
}

func (m *manifestCache) lookup(target, key string) (SnapshotSummary, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.target != target {
		return SnapshotSummary{}, false
	}
	summary, ok := m.summaries[key]
	return summary, ok
}

func (m *manifestCache) replace(target string, summaries map[string]SnapshotSummary) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.target = target
	m.summaries = summaries
}

// Summarize builds the Backups overview. It returns ErrTargetNotFound when
// no target is configured; a target that does not answer is not an error
// but a summary with Reachable false and no snapshot totals.
func (c *Controller) Summarize(ctx context.Context) (*TargetSummary, error) {
	credentials, err := c.deps.Targets.credentials(ctx, DefaultTargetName)
	if err != nil {
		return nil, err
	}
	summary := &TargetSummary{CheckedAt: time.Now()}
	if err := c.summarizeActivity(ctx, summary); err != nil {
		return nil, err
	}

	location := targetLocation(credentials)
	target, err := c.openStore(location)
	if err == nil {
		err = target.Reachable(ctx)
	}
	if err != nil {
		summary.UnreachableReason = err.Error()
		return summary, nil
	}
	summary.Reachable = true
	totals, err := c.snapshotTotals(ctx, target, location, credentials.Prefix)
	if err != nil {
		var unreachable *TargetUnreachableError
		if !errors.As(err, &unreachable) {
			return nil, err
		}
		summary.Reachable = false
		summary.UnreachableReason = unreachable.Err.Error()
		return summary, nil
	}
	summary.Snapshots = totals
	return summary, nil
}

// summarizeActivity fills the runs and schedules from the database.
func (c *Controller) summarizeActivity(ctx context.Context, summary *TargetSummary) error {
	if c.deps.Store == nil {
		return nil
	}
	since := summary.CheckedAt.Add(-summaryWindow)
	counts, err := c.deps.Store.CountBackupRuns(ctx, since)
	if err != nil {
		return fmt.Errorf("backup: count runs: %w", err)
	}
	summary.Runs = RunTotals{
		Since: since, Succeeded: counts.Succeeded, Failed: counts.Failed,
		BackingUp: counts.BackingUp, Restoring: counts.Restoring,
	}
	if summary.Runs.LastSucceeded, err = c.latestRun(ctx, StatusSucceeded); err != nil {
		return err
	}
	if summary.Runs.LastFailed, err = c.latestRun(ctx, StatusFailed); err != nil {
		return err
	}
	schedules, err := c.deps.Store.CountEnvironmentBackupSchedules(ctx)
	if err != nil {
		return fmt.Errorf("backup: count schedules: %w", err)
	}
	summary.Schedules = ScheduleTotals{Environments: schedules.Environments, Scheduled: schedules.Scheduled}
	return nil
}

func (c *Controller) latestRun(ctx context.Context, status string) (*RunInfo, error) {
	row, err := c.deps.Store.LatestFinishedBackup(ctx, status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: latest %s run: %w", status, err)
	}
	info := &RunInfo{Project: row.ProjectName, Environment: row.EnvironmentName}
	if row.FinishedAt != nil {
		info.FinishedAt = *row.FinishedAt
	}
	if row.Error != nil {
		info.Error = *row.Error
	}
	return info, nil
}

// environmentDirectory is one environment's place in the key layout.
type environmentDirectory struct {
	project, namespace string
}

// snapshotTotals walks the layout down to the snapshot directories (one
// listing per project and per environment, never the backed-up data) and
// adds up the manifests in them.
func (c *Controller) snapshotTotals(ctx context.Context, target objectStore, location s3Location, prefix string) (*SnapshotTotals, error) {
	base := path.Join(strings.Trim(prefix, "/"), "skali", layoutVersion) + "/"
	projects, err := target.ListPrefixes(ctx, base)
	if err != nil {
		return nil, &TargetUnreachableError{Err: err}
	}
	var directories []environmentDirectory
	for _, projectPrefix := range projects {
		project := path.Base(strings.TrimSuffix(projectPrefix, "/"))
		environments, err := c.projectEnvironments(ctx, target, prefix, project)
		if err != nil {
			return nil, err
		}
		for _, namespace := range environments {
			directories = append(directories, environmentDirectory{project: project, namespace: namespace})
		}
	}

	identity := location.Endpoint + "/" + location.Bucket
	var mu sync.Mutex
	seen := make(map[string]SnapshotSummary)
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(summaryConcurrency)
	for _, directory := range directories {
		group.Go(func() error {
			found, err := c.environmentSnapshots(groupCtx, target, identity, prefix, directory)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			for key, summary := range found {
				seen[key] = summary
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	c.manifests.replace(identity, seen)

	orphaned, err := c.orphanedNamespaces(ctx, directories)
	if err != nil {
		return nil, err
	}
	return totalSnapshots(seen, orphaned), nil
}

// environmentSnapshots lists one environment's manifests and summarizes
// each, reading only those the cache does not hold.
func (c *Controller) environmentSnapshots(ctx context.Context, target objectStore, identity, prefix string, directory environmentDirectory) (map[string]SnapshotSummary, error) {
	var keys []string
	err := target.List(ctx, snapshotPrefix(prefix, directory.project, directory.namespace), func(info objectInfo) error {
		if strings.HasSuffix(info.Key, ".json") {
			keys = append(keys, info.Key)
		}
		return nil
	})
	if err != nil {
		return nil, &TargetUnreachableError{Err: err}
	}
	found := make(map[string]SnapshotSummary, len(keys))
	for _, key := range keys {
		summary, ok := c.manifests.lookup(identity, key)
		if !ok {
			manifest, err := c.readManifest(ctx, target, key)
			if err != nil {
				var unsupported *UnsupportedManifestError
				if errors.As(err, &unsupported) {
					slog.WarnContext(ctx, "backup: skip unsupported manifest", "key", key, "err", err)
					continue
				}
				return nil, &TargetUnreachableError{Err: err}
			}
			summary = summarize(manifest)
		}
		// The layout's directory, not the manifest's display name, says
		// which project and environment namespace a snapshot belongs to.
		summary.Environment = directory.project + "/" + directory.namespace
		found[key] = summary
	}
	return found, nil
}

// orphanedNamespaces returns the "project/namespace" directories no live
// environment owns.
func (c *Controller) orphanedNamespaces(ctx context.Context, directories []environmentDirectory) (map[string]bool, error) {
	orphaned := make(map[string]bool)
	owners := make(map[string]map[string]store.Environment)
	for _, directory := range directories {
		projectOwners, ok := owners[directory.project]
		if !ok {
			var err error
			if projectOwners, err = c.backupOwners(ctx, directory.project); err != nil {
				return nil, err
			}
			owners[directory.project] = projectOwners
		}
		if _, owned := projectOwners[directory.namespace]; !owned {
			orphaned[directory.project+"/"+directory.namespace] = true
		}
	}
	return orphaned, nil
}

// totalSnapshots adds up summaries keyed by manifest key whose Environment
// holds the "project/namespace" directory.
func totalSnapshots(summaries map[string]SnapshotSummary, orphaned map[string]bool) *SnapshotTotals {
	totals := &SnapshotTotals{Projects: []ProjectSnapshotTotals{}}
	byProject := make(map[string]*ProjectSnapshotTotals)
	for _, summary := range summaries {
		totals.Count++
		totals.Bytes += summary.Bytes
		if summary.Trigger == TriggerScheduled {
			totals.Scheduled++
		} else {
			totals.Manual++
		}
		if orphaned[summary.Environment] {
			totals.Orphaned++
		}
		created := summary.CreatedAt
		if totals.OldestAt == nil || created.Before(*totals.OldestAt) {
			totals.OldestAt = &created
		}
		if totals.NewestAt == nil || created.After(*totals.NewestAt) {
			totals.NewestAt = &created
		}
		project, _, _ := strings.Cut(summary.Environment, "/")
		entry, ok := byProject[project]
		if !ok {
			entry = &ProjectSnapshotTotals{Project: project}
			byProject[project] = entry
		}
		entry.Count++
		entry.Bytes += summary.Bytes
		if created.After(entry.NewestAt) {
			entry.NewestAt = created
		}
	}
	for _, entry := range byProject {
		totals.Projects = append(totals.Projects, *entry)
	}
	sort.Slice(totals.Projects, func(i, j int) bool {
		a, b := totals.Projects[i], totals.Projects[j]
		if a.Bytes != b.Bytes {
			return a.Bytes > b.Bytes
		}
		return a.Project < b.Project
	})
	return totals
}
