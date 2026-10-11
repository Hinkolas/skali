package backup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Hinkolas/skali/internal/utils"
)

// SnapshotDamagedError reports snapshot data that cannot be restored as its
// manifest describes it: a malformed manifest, or a component object that
// is missing, has the wrong size, or does not match its recorded checksum.
// Verification runs before anything destructive, so the environment is
// untouched when it fails.
type SnapshotDamagedError struct {
	Snapshot string
	Reason   string
}

func (e *SnapshotDamagedError) Error() string {
	return fmt.Sprintf("snapshot %s is damaged: %s", e.Snapshot, e.Reason)
}

// validateManifest checks a restore's manifest against the key it was read
// from before any of its components is trusted: the id is the one the key
// names, every component is of a known kind and carries what restoring it
// needs, no service appears twice, and every object lies inside the
// snapshot's own environment directory, so a manifest cannot point a
// restore at another environment's data.
func validateManifest(m *Manifest, key string) error {
	damaged := func(format string, args ...any) error {
		return &SnapshotDamagedError{Snapshot: m.SnapshotID, Reason: fmt.Sprintf(format, args...)}
	}
	if id := snapshotIDFromManifestKey(key); m.SnapshotID != id {
		return damaged("the manifest at %s names snapshot %q", key, m.SnapshotID)
	}
	base, ok := strings.CutSuffix(key, "/snapshots/"+m.SnapshotID+".json")
	if !ok {
		return damaged("the manifest key %s is outside the snapshot layout", key)
	}
	base += "/"
	// Dot segments stay out too: some servers normalize them away.
	inside := func(object string) bool {
		rest, ok := strings.CutPrefix(object, base)
		if !ok || rest == "" || strings.HasPrefix(rest, "snapshots/") {
			return false
		}
		for _, segment := range strings.Split(rest, "/") {
			if segment == "." || segment == ".." {
				return false
			}
		}
		return true
	}
	seen := map[string]bool{}
	for _, component := range m.Components {
		label := componentLabel(component)
		switch component.Kind {
		case ComponentDatabase:
			if component.ServiceKey == "" || !inside(component.ObjectKey) {
				return damaged("database component %q has no dump inside the snapshot", component.ServiceKey)
			}
		case ComponentBucket:
			if component.ServiceKey == "" || !inside(component.ObjectPrefix) ||
				!strings.HasSuffix(component.ObjectPrefix, "/") || component.ObjectCount < 0 {
				return damaged("bucket component %q has no object prefix inside the snapshot", component.ServiceKey)
			}
		case ComponentVolume:
			if component.Application == "" || component.Volume == "" || !inside(component.ObjectKey) {
				return damaged("volume component %s.%s has no archive inside the snapshot",
					component.Application, component.Volume)
			}
		default:
			return damaged("component kind %q is unknown to this build", component.Kind)
		}
		if component.Bytes < 0 {
			return damaged("%s records a negative size", label)
		}
		if seen[label] {
			return damaged("%s appears twice", label)
		}
		seen[label] = true
	}
	return nil
}

// verifyComponent proves one component's stored data matches its manifest
// before the restore destroys anything: a database dump and a volume
// archive exist at their recorded size (and the dump with its recorded
// checksum), a bucket's objects add up to its recorded count and bytes.
// The returned listing describes a bucket's snapshot objects for the
// check after the copy; it is zero for the other kinds. The content of
// dumps and archives is verified again where it is read, in the Job,
// before the destructive command runs.
func verifyComponent(ctx context.Context, log copyLog, target objectStore, snapshot string,
	component Component) (bucketListing, error) {
	damaged := func(format string, args ...any) error {
		return &SnapshotDamagedError{Snapshot: snapshot, Reason: fmt.Sprintf(format, args...)}
	}
	label := componentLabel(component)
	if component.Kind == ComponentBucket {
		listing, err := listBucket(ctx, target, component.ObjectPrefix)
		if err != nil {
			return bucketListing{}, fmt.Errorf("list %s: %w", label, err)
		}
		if listing.Objects != component.ObjectCount || listing.Bytes != component.Bytes {
			return bucketListing{}, damaged("%s holds %d objects (%s); the manifest records %d (%s)",
				label, listing.Objects, utils.FormatBytes(listing.Bytes),
				component.ObjectCount, utils.FormatBytes(component.Bytes))
		}
		log.Info(ctx, fmt.Sprintf("%s: %d objects (%s) as recorded",
			label, listing.Objects, utils.FormatBytes(listing.Bytes)))
		return listing, nil
	}

	stat, err := target.Stat(ctx, component.ObjectKey)
	if errors.Is(err, errNotFound) {
		return bucketListing{}, damaged("%s is missing (%s)", label, component.ObjectKey)
	}
	if err != nil {
		return bucketListing{}, fmt.Errorf("check %s: %w", label, err)
	}
	if stat.Size != component.Bytes {
		return bucketListing{}, damaged("%s is %s; the manifest records %s",
			label, utils.FormatBytes(stat.Size), utils.FormatBytes(component.Bytes))
	}
	// The upload recorded the dump's checksum on the object, and the
	// backup copied it into the manifest: a difference means the object
	// was replaced since. The content itself is hashed on download.
	if component.SHA256 != "" && stat.SHA256 != "" && !strings.EqualFold(component.SHA256, stat.SHA256) {
		return bucketListing{}, damaged("%s carries checksum %s; the manifest records %s",
			label, stat.SHA256, component.SHA256)
	}
	note := fmt.Sprintf("%s: %s as recorded", label, utils.FormatBytes(stat.Size))
	if component.SHA256 != "" {
		note += ", sha256 " + utils.ShortChecksum(component.SHA256)
	}
	log.Info(ctx, note)
	return bucketListing{}, nil
}

// bucketListing summarizes a set of objects relative to a prefix: how many,
// their bytes, and an order-independent fingerprint of every key with its
// size. Two listings are equal exactly when, but for a hash collision, they
// hold the same keys at the same sizes.
type bucketListing struct {
	Objects     int64
	Bytes       int64
	Fingerprint uint64
}

func (l *bucketListing) add(key string, size int64) {
	sum := sha256.Sum256([]byte(key + "\x00" + strconv.FormatInt(size, 10)))
	l.Objects++
	l.Bytes += size
	l.Fingerprint += binary.BigEndian.Uint64(sum[:8])
}

// listBucket summarizes every object under prefix, keys relative to it.
func listBucket(ctx context.Context, store objectStore, prefix string) (bucketListing, error) {
	var listing bucketListing
	err := store.List(ctx, prefix, func(info objectInfo) error {
		listing.add(strings.TrimPrefix(info.Key, prefix), info.Size)
		return nil
	})
	return listing, err
}
