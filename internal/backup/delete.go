package backup

import (
	"context"
	"errors"
	"fmt"

	"github.com/Hinkolas/skali/internal/store"
)

// ErrSnapshotInUse: an unfinished restore reads the snapshot.
var ErrSnapshotInUse = errors.New("backup: snapshot is being restored")

// DeleteInput names one snapshot of a project to remove. Allowed, when
// set, sees the environment holding the snapshot before anything is
// deleted; an error it returns aborts the deletion untouched (the API uses
// it for the per-environment access check).
type DeleteInput struct {
	Project    string
	SnapshotID string
	Allowed    func(environment string) error
}

// DeleteSnapshot removes one snapshot from the backup target: the manifest
// first, so a half-deleted snapshot is never listed, then every component
// object. Snapshots an unfinished restore reads are refused. Any snapshot
// of the project may be deleted this way, manual or scheduled; retention
// is the only caller that restricts itself to scheduled ones.
func (c *Controller) DeleteSnapshot(ctx context.Context, in DeleteInput) error {
	credentials, target, err := c.openTarget(ctx)
	if err != nil {
		return err
	}
	key, environment, err := c.findSnapshot(ctx, target, credentials.Prefix, in.Project, in.SnapshotID)
	if err != nil {
		return err
	}
	if in.Allowed != nil {
		if err := in.Allowed(environment); err != nil {
			return err
		}
	}
	protected, err := c.snapshotsBeingRestored(ctx)
	if err != nil {
		return err
	}
	if protected[in.SnapshotID] {
		return ErrSnapshotInUse
	}
	manifest, err := c.readManifest(ctx, target, key)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return ErrSnapshotNotFound
		}
		return err
	}
	return deleteSnapshotObjects(ctx, target, key, manifest)
}

// deleteSnapshotObjects removes one snapshot in the inverse of the write
// order's guarantee: the manifest goes first so listings stop showing the
// snapshot before any data disappears, then the component objects. A crash
// in between leaves unreferenced objects, which the layout already declares
// garbage, never a listed snapshot with missing parts. Missing objects are
// fine: re-running a deletion is idempotent.
func deleteSnapshotObjects(ctx context.Context, target objectStore, manifestKey string, m *Manifest) error {
	if err := target.Remove(ctx, manifestKey); err != nil && !errors.Is(err, errNotFound) {
		return fmt.Errorf("remove manifest: %w", err)
	}
	for _, component := range m.Components {
		var err error
		switch {
		case component.ObjectPrefix != "":
			_, err = target.RemovePrefix(ctx, component.ObjectPrefix)
		case component.ObjectKey != "":
			err = target.Remove(ctx, component.ObjectKey)
		}
		if err != nil && !errors.Is(err, errNotFound) {
			return fmt.Errorf("remove %s: %w", componentLabel(component), err)
		}
	}
	return nil
}

// snapshotsBeingRestored lists the snapshot ids unfinished restore rows
// read, so neither a user nor retention deletes data mid-restore.
func (c *Controller) snapshotsBeingRestored(ctx context.Context) (map[string]bool, error) {
	rows, err := c.deps.Store.ListUnfinishedBackups(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup: list unfinished: %w", err)
	}
	return restoredSnapshotIDs(rows), nil
}

func restoredSnapshotIDs(rows []store.Backup) map[string]bool {
	protected := make(map[string]bool)
	for _, row := range rows {
		if row.Kind == KindRestore && row.SnapshotKey != "" {
			protected[snapshotIDFromManifestKey(row.SnapshotKey)] = true
		}
	}
	return protected
}
