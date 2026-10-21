package localdev

// skali dev upgrade moves an existing local platform to this CLI's release
// and k3s pin in place. Everything the developer built up lives on: the
// record and its credentials, the development CA (the state directory is
// never touched), and every byte inside the node's volumes (the platform
// database, project databases, buckets, application volumes, the registry).
// The release half is the same bundle converge a production update runs:
// skalid applies its database migrations at start, the operators roll to
// their new pins. The k3s half replaces the node container around its
// volumes (node.go), because a k3d node cannot change its image.
//
// Transitions only move forward: database migrations and k3s datastores do
// not go back, so a downgrade, a k3s jump of more than one minor version,
// and a switch between released and working-tree platforms are refused
// before anything changes. An upgrade is recorded before its first change
// and cleared after its health proof; while the record says unfinished,
// every other command refuses the platform and names the retry.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/kube"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
	"github.com/Hinkolas/skali/internal/version"
	"github.com/Hinkolas/skali/migrations"
)

// UpgradeRecord is an upgrade in progress: where it started and where it
// goes. The record already carries the target release while this is set,
// so an older CLI refuses the platform instead of running its skalid
// against a database the target may have migrated.
type UpgradeRecord struct {
	FromVersion     string    `json:"from_version,omitempty"`
	FromSkalidImage string    `json:"from_skalid_image"`
	FromK3sImage    string    `json:"from_k3s_image"`
	ToVersion       string    `json:"to_version,omitempty"`
	ToSkalidImage   string    `json:"to_skalid_image"`
	ToK3sImage      string    `json:"to_k3s_image"`
	StartedAt       time.Time `json:"started_at"`
}

func (u *UpgradeRecord) unfinishedError() error {
	return fmt.Errorf("the local platform's upgrade from %s to %s did not finish; "+
		"its data and development CA are retained; run skali dev upgrade with skali %s to finish it",
		releaseLabel(u.FromVersion), releaseLabel(u.ToVersion), releaseLabel(u.ToVersion))
}

// checkTransition refuses the release and k3s moves an upgrade cannot make,
// with the way forward; nil when skali dev upgrade can take the platform
// from its record to this CLI's release and pin.
func checkTransition(state *State) error {
	installed, target := state.Version, PlatformVersion()
	switch {
	case installed == target:
	case installed == "" || target == "":
		return fmt.Errorf("local dev platform runs %s and this CLI is %s; working-tree platforms have no release "+
			"to upgrade from or to, so only skali dev reset (deletes the local platform and its data) switches between them",
			releaseLabel(installed), releaseLabel(target))
	case version.Older(target, installed):
		return fmt.Errorf("local dev platform runs %s, newer than this CLI's %s, and its database migrations cannot be undone; "+
			"run it with skali %s (skali upgrade --version %s), or skali dev reset to delete the local platform and its data and recreate it at %s",
			installed, target, installed, installed, target)
	case !version.IsRelease(installed) || !version.IsRelease(target):
		return fmt.Errorf("local dev platform runs %s and this CLI is %s; only tagged releases can be compared for an upgrade; run skali dev reset",
			installed, target)
	}
	return checkK3sTransition(state.K3sImage, PlatformK3sImage())
}

// k3sPattern reads a k3s image reference: repository and the
// v<major>.<minor>.<patch>-k3s<revision> tag.
var k3sPattern = regexp.MustCompile(`^(.+):v(\d+)\.(\d+)\.(\d+)-k3s(\d+)$`)

func parseK3sImage(image string) (repository string, release [4]int, ok bool) {
	match := k3sPattern.FindStringSubmatch(image)
	if match == nil {
		return "", release, false
	}
	for i := range release {
		release[i], _ = strconv.Atoi(match[i+2])
	}
	return match[1], release, true
}

// checkK3sTransition allows what Kubernetes supports in place: the same
// release, a newer patch or k3s revision, or the next minor version.
func checkK3sTransition(installed, target string) error {
	if installed == target {
		return nil
	}
	fromRepository, from, okFrom := parseK3sImage(installed)
	toRepository, to, okTo := parseK3sImage(target)
	if !okFrom || !okTo || fromRepository != toRepository {
		return fmt.Errorf("local dev platform runs k3s image %s and this CLI pins %s; they are not two releases of one k3s, "+
			"so only skali dev reset (deletes the local platform and its data) moves between them", installed, target)
	}
	if slices.Compare(to[:], from[:]) < 0 {
		return fmt.Errorf("local dev platform runs %s, newer than this CLI's %s, and k3s cannot be downgraded; "+
			"use the skali release that pins it, or skali dev reset to delete the local platform and its data", installed, target)
	}
	if to[0] != from[0] || to[1] > from[1]+1 {
		return fmt.Errorf("local dev platform runs %s and this CLI pins %s; Kubernetes upgrades one minor version at a time, "+
			"so upgrade it with a skali release that pins k3s v%d.%d first", installed, target, from[0], from[1]+1)
	}
	return nil
}

// UpgradePlan is what skali dev upgrade will do, computed without changing
// anything.
type UpgradePlan struct {
	Status          ClusterStatus
	FromVersion     string
	ToVersion       string
	FromSkalidImage string
	ToSkalidImage   string
	FromK3sImage    string
	ToK3sImage      string
	// Resuming is the unfinished upgrade this run completes.
	Resuming *UpgradeRecord
	// Components are the platform images whose pins move.
	Components []ComponentChange
	// SchemaInstalled is the platform database's migration version, -1
	// when it cannot be read (a stopped platform); SchemaTarget is the
	// newest migration this CLI's skalid carries.
	SchemaInstalled int
	SchemaTarget    int
	CA              *CA
}

// ComponentChange is one platform image moving between pins; From is
// empty for a component the installed platform does not have yet.
type ComponentChange struct {
	Repository string
	From       string
	To         string
}

// ReleaseChange reports a control-plane move.
func (p *UpgradePlan) ReleaseChange() bool {
	return p.FromVersion != p.ToVersion || p.FromSkalidImage != p.ToSkalidImage
}

// K3sChange reports a node replacement.
func (p *UpgradePlan) K3sChange() bool { return p.FromK3sImage != p.ToK3sImage }

// Current reports a platform with nothing to upgrade.
func (p *UpgradePlan) Current() bool {
	return p.Resuming == nil && !p.ReleaseChange() && !p.K3sChange()
}

// PlanUpgrade validates the move from the installed platform to this CLI's
// release, k3s pin, and skalidImage, and describes it. It changes nothing.
func PlanUpgrade(ctx context.Context, skalidImage string) (*UpgradePlan, error) {
	state, err := LoadState()
	if errors.Is(err, ErrNotInstalled) {
		return nil, errors.New("no local platform is installed; skali dev creates one at this release")
	}
	if err != nil {
		return nil, err
	}
	if err := checkUpgradable(state); err != nil {
		return nil, err
	}
	ca, err := LoadCA()
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			certPath, _, _ := CAPaths()
			return nil, fmt.Errorf("the local platform's development CA is missing (%s); an upgrade cannot recreate it; "+
				"run skali dev reset to delete the local platform and its data, then skali dev to recreate it", certPath)
		}
		return nil, err
	}
	status, err := Status(ctx)
	if err != nil {
		return nil, err
	}
	if status == ClusterAbsent {
		return nil, fmt.Errorf("the local platform's cluster %s is missing, so there is nothing to upgrade in place; "+
			"skali dev reset clears the record, then skali dev creates the platform at this release", ClusterName())
	}
	plan := &UpgradePlan{
		Status:          status,
		FromVersion:     state.Version,
		ToVersion:       PlatformVersion(),
		FromSkalidImage: state.SkalidImage,
		ToSkalidImage:   skalidImage,
		FromK3sImage:    state.K3sImage,
		ToK3sImage:      PlatformK3sImage(),
		Resuming:        state.Upgrade,
		Components:      componentChanges(state.ImportedImages, RequiredImages()),
		SchemaInstalled: -1,
		SchemaTarget:    latestMigration(),
		CA:              ca,
	}
	if resuming := state.Upgrade; resuming != nil {
		plan.FromVersion = resuming.FromVersion
		plan.FromSkalidImage = resuming.FromSkalidImage
		plan.FromK3sImage = resuming.FromK3sImage
	}
	if status == ClusterRunning {
		plan.SchemaInstalled = installedSchema(ctx)
	}
	return plan, nil
}

// checkUpgradable is CheckVersion without the version equality: the record
// must be consistent and the move to this CLI supported. An unfinished
// upgrade may only be finished forward (its record already carries its
// target), never moved back to where it came from.
func checkUpgradable(state *State) error {
	if err := checkTransition(state); err != nil {
		return err
	}
	return checkRecord(state)
}

// componentChanges pairs the platform images a cluster holds with the ones
// this CLI requires by repository: a repository whose tag moved is one
// change, a repository the cluster lacks is a new component. The k3s
// built-ins ride along, so a k3s move also lists its bundled Traefik.
func componentChanges(installed, required []string) []ComponentChange {
	held := make(map[string]bool, len(installed))
	for _, image := range installed {
		held[image] = true
	}
	needed := make(map[string]bool, len(required))
	for _, image := range required {
		needed[image] = true
	}
	// Retired tags per repository, in the order the cluster recorded them.
	retired := map[string][]string{}
	for _, image := range installed {
		if !needed[image] {
			repository, tag := splitImage(image)
			retired[repository] = append(retired[repository], tag)
		}
	}
	var changes []ComponentChange
	for _, image := range required {
		if held[image] {
			continue
		}
		repository, tag := splitImage(image)
		change := ComponentChange{Repository: repository, To: tag}
		if tags := retired[repository]; len(tags) > 0 {
			change.From, retired[repository] = tags[0], tags[1:]
		}
		changes = append(changes, change)
	}
	return changes
}

// splitImage separates a reference's tag from its repository; a registry
// port is not a tag.
func splitImage(image string) (repository, tag string) {
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon <= slash {
		return image, ""
	}
	return image[:colon], image[colon+1:]
}

// latestMigration is the newest platform database migration this CLI's
// skalid applies at start.
func latestMigration() int {
	files, _ := fs.Glob(migrations.FS, "*.sql")
	latest := 0
	for _, file := range files {
		number, _, _ := strings.Cut(file, "_")
		if value, err := strconv.Atoi(number); err == nil && value > latest {
			latest = value
		}
	}
	return latest
}

// installedSchema reads the platform database's applied migration version
// from its primary instance; -1 when it cannot.
func installedSchema(ctx context.Context) int {
	kubeconfig, err := KubeconfigPath()
	if err != nil {
		return -1
	}
	client, err := kube.New(kubeconfig)
	if err != nil {
		return -1
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := client.ExecInPod(ctx, bundle.Namespace, cnpg.PrimarySelector("skali-db"), cnpg.PostgresContainer,
		cnpg.PSQLQuery("skali", "SELECT max(version_id) FROM goose_db_version WHERE is_applied"))
	if err != nil {
		return -1
	}
	value, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return -1
	}
	return value
}

// UpgradeOptions parameterize one Upgrade.
type UpgradeOptions struct {
	// SkalidImage is the target control-plane image, present in the host
	// docker daemon.
	SkalidImage string
	Progress    Progress
}

// Upgrade carries out PlanUpgrade's plan, resumably: the upgrade is
// recorded before the first change, every step is idempotent, and the
// record clears only after the platform proved healthy at the target.
// A failure leaves the record set and the data in place; running Upgrade
// again continues from wherever the last attempt stopped.
func Upgrade(ctx context.Context, opts UpgradeOptions) (*State, error) {
	ctx, unlock, err := LockContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	progress := opts.Progress
	if progress == nil {
		progress = silentProgress{}
	}
	state, err := LoadState()
	if err != nil {
		return nil, err
	}
	if err := checkUpgradable(state); err != nil {
		return nil, err
	}
	if opts.SkalidImage == "" {
		return nil, errors.New("localdev: upgrade needs a target skalid image")
	}
	targetK3s := PlatformK3sImage()
	k3sChange := state.K3sImage != targetK3s

	// Everything that needs the network happens before the first change,
	// so a pull failure leaves the platform exactly as it was.
	pulls := []string{opts.SkalidImage}
	if k3sChange {
		pulls = append(pulls, targetK3s)
	}
	for _, image := range RequiredImages() {
		if !slices.Contains(state.ImportedImages, image) {
			pulls = append(pulls, image)
		}
	}
	progress.Start("Pull target images")
	if err := pullTargetImages(ctx, opts.SkalidImage, pulls); err != nil {
		return nil, err
	}
	progress.Done(fmt.Sprintf("%d on the host", len(pulls)))

	// Only an upgrade this run starts may be rolled back by revertUntouched:
	// an earlier attempt may have run the target's migrations already.
	fresh := state.Upgrade == nil
	if fresh {
		state.Upgrade = &UpgradeRecord{
			FromVersion:     state.Version,
			FromSkalidImage: state.SkalidImage,
			FromK3sImage:    state.K3sImage,
			StartedAt:       time.Now(),
		}
	}
	state.Upgrade.ToVersion = PlatformVersion()
	state.Upgrade.ToSkalidImage = opts.SkalidImage
	state.Upgrade.ToK3sImage = targetK3s
	state.Version = PlatformVersion()
	state.SkalidImage = opts.SkalidImage
	if err := SaveState(state); err != nil {
		return nil, err
	}

	if k3sChange {
		progress.Start("Replace node " + nodeContainer())
		progress.Note(state.K3sImage + " -> " + targetK3s + ", volumes kept")
		if err := replaceNode(ctx, targetK3s); err != nil {
			if fresh {
				err = revertUntouched(ctx, state, err)
			}
			return nil, err
		}
		state.K3sImage = targetK3s
		if err := SaveState(state); err != nil {
			return nil, err
		}
		progress.Done(targetK3s)
	}

	// The rest is the ordinary forced Ensure at the target: start, image
	// imports, the full converge (operators, skalid with its migrations),
	// and the health proof through the TLS edge.
	if _, err := ensure(ctx, EnsureOptions{
		SkalidImage:   opts.SkalidImage,
		ForceConverge: true,
		Progress:      progress,
	}, true); err != nil {
		return nil, err
	}
	state, err = LoadState()
	if err != nil {
		return nil, err
	}
	state.Upgrade = nil
	if err := SaveState(state); err != nil {
		return nil, err
	}
	if err := removeNodeSpec(); err != nil {
		return nil, err
	}
	return state, nil
}

// revertUntouched handles a node replacement that failed before the old
// node was removed. Nothing but the record changed then (the release moves
// only after the replacement), so the record goes back to the installed
// platform and its CLI keeps working; the staging container and capture go
// with it. A failure past that point keeps the record for the retry.
func revertUntouched(ctx context.Context, state *State, cause error) error {
	ctx = context.WithoutCancel(ctx)
	upgrade := state.Upgrade
	node, err := inspectContainer(ctx, nodeContainer())
	if err != nil || node.Config.Image != upgrade.FromK3sImage {
		return cause
	}
	_ = removeContainer(ctx, nodeContainer()+"-upgrade")
	state.Version = upgrade.FromVersion
	state.SkalidImage = upgrade.FromSkalidImage
	state.K3sImage = upgrade.FromK3sImage
	state.Upgrade = nil
	if err := SaveState(state); err != nil {
		return errors.Join(cause, err)
	}
	if err := removeNodeSpec(); err != nil {
		return errors.Join(cause, err)
	}
	return fmt.Errorf("%w\nthe local platform is unchanged at %s; if it is stopped, skali dev start with that release starts it again",
		cause, releaseLabel(upgrade.FromVersion))
}

// pullTargetImages makes every image of the target present on the host. A
// published skalid release and the public platform images pull; any other
// skalid image (a working-tree build) must already be there.
func pullTargetImages(ctx context.Context, skalidImage string, images []string) error {
	var public []string
	for _, image := range images {
		if image == skalidImage {
			if _, published := version.PublishedSkalidVersion(image); !published {
				if _, err := ImageID(ctx, image); err != nil {
					return err
				}
				continue
			}
		}
		public = append(public, image)
	}
	return ensureHostImages(ctx, public)
}
