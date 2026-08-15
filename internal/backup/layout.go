package backup

import (
	"fmt"
	"path"
	"strings"
)

// The S3 layout is self-describing and keyed by project and environment
// NAME, not UUID: a fresh installation, whose database knows nothing about
// old snapshots, lists and restores purely from the bucket.
//
//	<prefix>/skali/v1/<project>/<environment>/snapshots/<snapshot-id>.json
//	<prefix>/skali/v1/<project>/<environment>/databases/<service-key>/<snapshot-id>.dump
//	<prefix>/skali/v1/<project>/<environment>/buckets/<service-key>/<snapshot-id>/<object-key>
//	<prefix>/skali/v1/<project>/<environment>/volumes/<app-key>/<volume-key>/<snapshot-id>.tar.zst
//
// The manifest is written last: its existence certifies a complete
// snapshot, and objects unreferenced by any manifest are garbage.

// layoutVersion is the key-layout generation, independent of the manifest
// format version inside the documents.
const layoutVersion = "v1"

func projectBase(prefix, project string) string {
	return path.Join(strings.Trim(prefix, "/"), "skali", layoutVersion, project)
}

func environmentBase(prefix, project, environment string) string {
	return path.Join(projectBase(prefix, project), environment)
}

// environmentFromBase inverts environmentBase for a listed environment
// prefix ("<base>/<environment>/" -> "<environment>").
func environmentFromBase(environmentPrefix string) string {
	return path.Base(strings.TrimSuffix(environmentPrefix, "/"))
}

func snapshotPrefix(prefix, project, environment string) string {
	return environmentBase(prefix, project, environment) + "/snapshots/"
}

func manifestKey(prefix, project, environment, snapshotID string) string {
	return snapshotPrefix(prefix, project, environment) + snapshotID + ".json"
}

func databaseKey(prefix, project, environment, serviceKey, snapshotID string) string {
	return fmt.Sprintf("%s/databases/%s/%s.dump",
		environmentBase(prefix, project, environment), serviceKey, snapshotID)
}

func bucketPrefixKey(prefix, project, environment, serviceKey, snapshotID string) string {
	return fmt.Sprintf("%s/buckets/%s/%s/",
		environmentBase(prefix, project, environment), serviceKey, snapshotID)
}

func volumeKey(prefix, project, environment, appKey, volume, snapshotID string) string {
	return fmt.Sprintf("%s/volumes/%s/%s/%s.tar.zst",
		environmentBase(prefix, project, environment), appKey, volume, snapshotID)
}

// snapshotIDFromManifestKey inverts manifestKey for listings.
func snapshotIDFromManifestKey(key string) string {
	base := path.Base(key)
	return strings.TrimSuffix(base, ".json")
}
