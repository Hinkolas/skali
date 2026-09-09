package updates

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/version"
)

var (
	// ErrScanDisabled means no feed is configured (SKALI_UPDATE_SCAN=false).
	ErrScanDisabled = errors.New("update scanning is disabled on this installation")
	// ErrBusy refuses an update while runs are in flight: the control-plane
	// roll would interrupt them.
	ErrBusy = errors.New("deployments or backups are in flight; try again when they finish")
	// ErrNotNewer refuses a version that is not ahead of the running one.
	ErrNotNewer = errors.New("that version is not newer than the installed one")
)

// BlockedError explains why an update cannot start; the API answers it as
// update_blocked with the message.
type BlockedError struct{ Reason string }

func (e *BlockedError) Error() string { return e.Reason }

// Settings is the operator's persisted choice plus the last scan.
type Settings struct {
	Channel       Channel    `json:"channel"`
	AutoUpdate    bool       `json:"auto_update"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	// LastError says why the last scan did not complete, LastErrorKind
	// classifies it (offline, not_found, rate_limited, unavailable,
	// invalid) so the console can word the notice; both empty when it did.
	LastError     string        `json:"last_error,omitempty"`
	LastErrorKind FeedErrorKind `json:"last_error_kind,omitempty"`
	// Latest is the newest release the last scan found on the channel,
	// whether or not it is newer than what runs.
	Latest *Release `json:"latest"`
}

// Status is the whole document the console renders.
type Status struct {
	Summary   Summary   `json:"summary"`
	Installed Installed `json:"installed"`
	Settings
	// UpdateAvailable includes an available release or an incomplete update.
	UpdateAvailable bool `json:"update_available"`
	// Managed is whether a cluster coordinator exists to perform updates;
	// Manageable whether one could start right now, Reason why not.
	Managed    bool        `json:"managed"`
	Manageable bool        `json:"manageable"`
	Reason     string      `json:"reason,omitempty"`
	Nodes      []NodeState `json:"nodes"`
	// Operation is the running update, or the last one when none runs.
	Operation      *OperationState `json:"operation"`
	LastSuccessful *OperationState `json:"last_successful"`
}

// Installed is what this daemon knows it runs.
type Installed struct {
	Version string `json:"version"`
	// PlatformVersion is the release the cluster state records; empty when
	// unmanaged or initialized by a dev build.
	PlatformVersion string `json:"platform_version,omitempty"`
}

// Service owns the update lifecycle: scan, settings, apply, and the daily
// loop. It is safe to construct without a Feed (scanning disabled) or a
// Cluster (unmanaged); both degrade to honest status fields.
type Service struct {
	Store   *store.Store
	Feed    Feed
	Cluster *Cluster
	// Version is the running daemon's version, one component of convergence.
	Version string
	// ScanInterval is how often the loop scans; zero means daily.
	ScanInterval time.Duration
	Logger       *slog.Logger
	now          func() time.Time
}

const (
	defaultScanInterval = 24 * time.Hour
	loopTick            = time.Hour
	bootDelay           = 30 * time.Second
)

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Status assembles the document from the settings row and the cluster.
func (s *Service) Status(ctx context.Context) (*Status, error) {
	row, err := s.Store.GetUpdateSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("read update settings: %w", err)
	}
	return s.status(ctx, row)
}

func (s *Service) status(ctx context.Context, row store.UpdateSetting) (*Status, error) {
	status := &Status{
		Installed: Installed{Version: s.Version},
		Settings:  settingsFromRow(row),
		Nodes:     []NodeState{},
	}
	// Only a released daemon can be behind a release: a working-tree build
	// has no place in the version order, so it shows what the feed found
	// without claiming an update.
	if version.IsRelease(s.Version) && status.Latest != nil && version.Older(s.Version, status.Latest.Version) {
		status.UpdateAvailable = true
	}
	snapshot, err := s.Cluster.Snapshot(ctx)
	switch {
	case errors.Is(err, ErrNotManaged):
		status.Reason = "updates from the console need a coordinator-managed cluster; " +
			"run skali cluster upgrade on each host, or skali dev upgrade locally"
	case err != nil:
		status.Reason = "cluster state unavailable: " + err.Error()
	default:
		status.Managed = true
		status.Manageable = snapshot.Manageable
		status.Reason = snapshot.Reason
		status.Nodes = snapshot.Nodes
		status.Operation = snapshot.Operation
		status.LastSuccessful = snapshot.LastSuccessful
		status.Installed.PlatformVersion = snapshot.PlatformVersion
	}
	expectedK3s := ""
	if snapshot != nil {
		expectedK3s = snapshot.ExpectedK3s
		if expectedK3s == "" && snapshot.PlatformVersion == s.Version {
			expectedK3s = installer.K3sVersion
		}
	}
	status.Summary = summarize(status, expectedK3s, s.clock())
	if status.Managed {
		status.UpdateAvailable = status.Summary.Action == "update" || status.Summary.Action == "finish"
	}
	return status, nil
}

func settingsFromRow(row store.UpdateSetting) Settings {
	settings := Settings{
		Channel: Channel(row.Channel), AutoUpdate: row.AutoUpdate,
		LastCheckedAt: row.LastCheckedAt,
	}
	if row.LastError != nil {
		settings.LastError = *row.LastError
	}
	if row.LastErrorKind != nil {
		settings.LastErrorKind = FeedErrorKind(*row.LastErrorKind)
	}
	if row.LatestVersion != nil && *row.LatestVersion != "" {
		settings.Latest = &Release{
			Version: *row.LatestVersion, Prerelease: version.IsPrerelease(*row.LatestVersion),
		}
		if row.LatestPublishedAt != nil {
			settings.Latest.PublishedAt = *row.LatestPublishedAt
		}
		if row.LatestUrl != nil {
			settings.Latest.URL = *row.LatestUrl
		}
		if row.LatestK3s != nil {
			settings.Latest.K3s = *row.LatestK3s
		}
	}
	return settings
}

// Scan asks the feed for the channel's newest release and records the
// answer; a feed failure is recorded too (classified, see FeedError), never
// returned as a scan error, because the last known release stays useful.
func (s *Service) Scan(ctx context.Context) (*Status, error) {
	if s.Feed == nil {
		return nil, ErrScanDisabled
	}
	row, err := s.Store.GetUpdateSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("read update settings: %w", err)
	}
	release, feedErr := s.Feed.Latest(ctx, Channel(row.Channel))
	params := store.RecordUpdateScanParams{}
	if feedErr != nil {
		failure := classifyFeedError(feedErr)
		message, kind := failure.Error(), string(failure.Kind)
		params.LastError, params.LastErrorKind = &message, &kind
	} else if release != nil {
		params.LatestVersion = &release.Version
		params.LatestPublishedAt = &release.PublishedAt
		params.LatestUrl = &release.URL
		params.LatestK3s = &release.K3s
	} else {
		// No release on the channel yet: forget a stale one rather than
		// keep announcing it.
		empty := ""
		params.LatestVersion = &empty
	}
	row, err = s.Store.RecordUpdateScan(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("record update scan: %w", err)
	}
	return s.status(ctx, row)
}

// UpdateSettings persists the channel and auto-update choice. A channel
// change invalidates the last scan's answer, so it rescans when it can.
func (s *Service) UpdateSettings(ctx context.Context, channel Channel, autoUpdate bool) (*Status, error) {
	if _, err := ParseChannel(string(channel)); err != nil {
		return nil, err
	}
	previous, err := s.Store.GetUpdateSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("read update settings: %w", err)
	}
	row, err := s.Store.SaveUpdateSettings(ctx, store.SaveUpdateSettingsParams{
		Channel: string(channel), AutoUpdate: autoUpdate,
	})
	if err != nil {
		return nil, fmt.Errorf("save update settings: %w", err)
	}
	if previous.Channel != row.Channel && s.Feed != nil {
		if status, err := s.Scan(ctx); err == nil {
			return status, nil
		}
	}
	return s.status(ctx, row)
}

// Apply asks the coordinator to move the platform to target. Every refusal
// is a typed error the API maps: unmanaged clusters, in-flight runs, a
// version that is not newer, and the cluster state's own conditions.
func (s *Service) Apply(ctx context.Context, target string) (*Status, error) {
	if !version.IsRelease(target) {
		return nil, &BlockedError{Reason: fmt.Sprintf("%q is not a tagged release", target)}
	}
	current, err := s.Status(ctx)
	if err != nil {
		return nil, err
	}
	if err := ValidateTarget(current, target); err != nil {
		return nil, err
	}
	snapshot, err := s.Cluster.Snapshot(ctx)
	if errors.Is(err, ErrNotManaged) {
		return nil, &BlockedError{Reason: "updates from the console need a coordinator-managed cluster"}
	}
	if err != nil {
		return nil, err
	}
	if !snapshot.Manageable {
		if snapshot.Operation != nil && !snapshot.Operation.Settled() {
			return nil, clusterstate.ErrOperationActive
		}
		return nil, &BlockedError{Reason: snapshot.Reason}
	}
	running, err := s.Store.CountRunningRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("count running runs: %w", err)
	}
	if running > 0 {
		return nil, ErrBusy
	}
	backups, err := s.Store.ListUnfinishedBackups(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unfinished backups: %w", err)
	}
	if len(backups) > 0 {
		return nil, ErrBusy
	}
	if _, err := s.Cluster.Apply(ctx, target); err != nil {
		if errors.Is(err, clusterstate.ErrOperationActive) {
			return nil, err
		}
		return nil, &BlockedError{Reason: err.Error()}
	}
	s.log().InfoContext(ctx, "platform update requested", "target", target, "from", s.Version)
	return s.Status(ctx)
}

// Resume re-freezes a failed update so its remaining steps run again; the
// proven ones are never replayed. It is Apply for the operation that
// already exists.
func (s *Service) Resume(ctx context.Context) (*Status, error) {
	if s.Cluster == nil || s.Cluster.Client == nil {
		return nil, &BlockedError{Reason: "updates from the console need a coordinator-managed cluster"}
	}
	store := &clusterstate.Store{Client: s.Cluster.Client}
	running, err := s.Store.CountRunningRuns(ctx)
	if err != nil {
		return nil, err
	}
	backups, err := s.Store.ListUnfinishedBackups(ctx)
	if err != nil {
		return nil, err
	}
	if running > 0 || len(backups) > 0 {
		return nil, ErrBusy
	}
	_, err = store.Update(ctx, func(state *clusterstate.State) error {
		if state.CurrentOperation == "" {
			return errors.New("no update is waiting to be resumed")
		}
		operation := state.Operations[state.CurrentOperation]
		if !clusterstate.IsReleaseOperation(state, operation) {
			return errors.New("the active operation is a topology change, not a software update")
		}
		if operation.Phase != clusterstate.OperationFailed {
			return clusterstate.ErrOperationActive
		}
		target := state.Revisions[operation.TargetRevision].Platform.Version
		if !version.IsRelease(s.Version) || version.Older(target, s.Version) {
			return fmt.Errorf("cannot resume update to %s while skalid runs %s", target, s.Version)
		}
		_, err := clusterstate.ResumeRelease(state, s.clock())
		return err
	})
	if err != nil {
		if errors.Is(err, clusterstate.ErrOperationActive) {
			return nil, err
		}
		return nil, &BlockedError{Reason: err.Error()}
	}
	return s.Status(ctx)
}

// Run is the daily loop: a scan at boot when the last one is older than
// the interval, then hourly checks of the same rule, so a restart never
// hammers the feed and a long-running daemon never drifts. A scan that
// failed is retried on every hourly check instead, so an outage of the
// update servers clears within the hour of their return. With auto-update
// on, a newer release that the cluster can take is applied right away;
// anything in the way is logged and retried on the next due scan.
func (s *Service) Run(ctx context.Context) {
	if s.Feed == nil {
		return
	}
	timer := time.NewTimer(bootDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	s.tick(ctx)
	ticker := time.NewTicker(loopTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Service) tick(ctx context.Context) {
	row, err := s.Store.GetUpdateSettings(ctx)
	if err != nil {
		s.log().WarnContext(ctx, "read update settings", "err", err)
		return
	}
	interval := s.ScanInterval
	if interval <= 0 {
		interval = defaultScanInterval
	}
	if row.LastError != nil && interval > loopTick {
		interval = loopTick
	}
	if row.LastCheckedAt != nil && s.clock().Sub(*row.LastCheckedAt) < interval {
		return
	}
	tickCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	status, err := s.Scan(tickCtx)
	if err != nil {
		s.log().WarnContext(ctx, "scan for updates", "err", err)
		return
	}
	if status.LastError != "" {
		s.log().WarnContext(ctx, "update scan failed", "kind", status.LastErrorKind, "err", status.LastError)
		return
	}
	if !status.AutoUpdate || status.Summary.Action != "update" {
		return
	}
	if status.Operation != nil && !status.Operation.Settled() {
		return
	}
	if _, err := s.Apply(tickCtx, status.Latest.Version); err != nil {
		s.log().InfoContext(ctx, "automatic update deferred", "target", status.Latest.Version, "reason", err)
		return
	}
	s.log().InfoContext(ctx, "automatic update started", "target", status.Latest.Version)
}
