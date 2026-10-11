package backup

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// validManifest is a manifest of snapshot "snap" of demo/production with
// one component of each kind, read from its standard key.
func validManifest() (*Manifest, string) {
	m := &Manifest{
		FormatVersion: ManifestFormatVersion,
		SnapshotID:    "snap",
		Project:       "demo",
		Environment:   "production",
		Components: []Component{
			{Kind: ComponentDatabase, ServiceKey: "data", Bytes: 4,
				ObjectKey: databaseKey("p", "demo", "production", "data", "snap")},
			{Kind: ComponentBucket, ServiceKey: "files", Bytes: 2, ObjectCount: 2,
				ObjectPrefix: bucketPrefixKey("p", "demo", "production", "files", "snap")},
			{Kind: ComponentVolume, Application: "web", Volume: "data", Bytes: 9,
				ObjectKey: volumeKey("p", "demo", "production", "web", "data", "snap")},
		},
	}
	return m, manifestKey("p", "demo", "production", "snap")
}

// A manifest is checked against the key it was read from before any of its
// components is trusted: a component outside the snapshot's own
// environment directory would let a restore read another environment's
// data, and malformed components would be restored as something else.
func TestValidateManifest(t *testing.T) {
	m, key := validManifest()
	require.NoError(t, validateManifest(m, key))

	other := environmentBase("p", "demo", "staging")
	for name, mutate := range map[string]func(m *Manifest) string{
		"id differs from the key": func(m *Manifest) string { m.SnapshotID = "other"; return key },
		"key outside the layout":  func(*Manifest) string { return "p/somewhere/snap.json" },
		"dump of another environment": func(m *Manifest) string {
			m.Components[0].ObjectKey = other + "/databases/data/snap.dump"
			return key
		},
		"bucket of another environment": func(m *Manifest) string {
			m.Components[1].ObjectPrefix = other + "/buckets/files/snap/"
			return key
		},
		"dot segment out of the snapshot": func(m *Manifest) string {
			m.Components[2].ObjectKey = environmentBase("p", "demo", "production") + "/volumes/../../staging/x.tar.zst"
			return key
		},
		"component under snapshots": func(m *Manifest) string {
			m.Components[0].ObjectKey = environmentBase("p", "demo", "production") + "/snapshots/other.json"
			return key
		},
		"bucket prefix without a slash": func(m *Manifest) string {
			m.Components[1].ObjectPrefix = "p/skali/v1/demo/production/buckets/files/snap"
			return key
		},
		"database without a service": func(m *Manifest) string { m.Components[0].ServiceKey = ""; return key },
		"volume without an app":      func(m *Manifest) string { m.Components[2].Application = ""; return key },
		"negative size":              func(m *Manifest) string { m.Components[2].Bytes = -1; return key },
		"negative count":             func(m *Manifest) string { m.Components[1].ObjectCount = -1; return key },
		"unknown kind":               func(m *Manifest) string { m.Components[2].Kind = "queue"; return key },
		"service twice": func(m *Manifest) string {
			m.Components = append(m.Components, m.Components[0])
			return key
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := validManifest()
			key := mutate(m)
			var damaged *SnapshotDamagedError
			require.ErrorAs(t, validateManifest(m, key), &damaged)
		})
	}
}

// Verification compares each stored object with the manifest's record of
// it, before anything is stopped: a missing, truncated, or replaced dump,
// a missing archive, and a bucket that lost or gained an object are all
// damage; matching data passes.
func TestVerifyComponent(t *testing.T) {
	ctx := context.Background()
	m, _ := validManifest()
	database, bucket, volume := m.Components[0], m.Components[1], m.Components[2]
	database.SHA256 = "aaaa"
	fill := func() *memoryStore {
		target := newMemoryStore()
		require.NoError(t, target.Put(ctx, database.ObjectKey, bytes.NewReader([]byte("dump")), 4))
		target.record(database.ObjectKey, "AAAA")
		require.NoError(t, target.Put(ctx, bucket.ObjectPrefix+"a", bytes.NewReader([]byte("a")), 1))
		require.NoError(t, target.Put(ctx, bucket.ObjectPrefix+"b", bytes.NewReader([]byte("b")), 1))
		require.NoError(t, target.Put(ctx, volume.ObjectKey, bytes.NewReader([]byte("archive!!")), 9))
		return target
	}

	target := fill()
	for _, component := range []Component{database, bucket, volume} {
		_, err := verifyComponent(ctx, &quietLog{}, target, "snap", component)
		require.NoError(t, err, componentLabel(component))
	}
	listing, err := verifyComponent(ctx, &quietLog{}, target, "snap", bucket)
	require.NoError(t, err)
	require.EqualValues(t, 2, listing.Objects)
	require.NotZero(t, listing.Fingerprint)

	for name, tc := range map[string]struct {
		damage    func(*memoryStore)
		component Component
	}{
		"missing dump": {func(s *memoryStore) { _ = s.Remove(ctx, database.ObjectKey) }, database},
		"truncated dump": {func(s *memoryStore) {
			_ = s.Put(ctx, database.ObjectKey, bytes.NewReader([]byte("du")), 2)
		}, database},
		"replaced dump":  {func(s *memoryStore) { s.record(database.ObjectKey, "bbbb") }, database},
		"missing object": {func(s *memoryStore) { _ = s.Remove(ctx, bucket.ObjectPrefix+"a") }, bucket},
		"extra object": {func(s *memoryStore) {
			_ = s.Put(ctx, bucket.ObjectPrefix+"c", bytes.NewReader(nil), 0)
		}, bucket},
		"resized object": {func(s *memoryStore) {
			_ = s.Put(ctx, bucket.ObjectPrefix+"a", bytes.NewReader([]byte("aa")), 2)
		}, bucket},
		"missing archive": {func(s *memoryStore) { _ = s.Remove(ctx, volume.ObjectKey) }, volume},
	} {
		t.Run(name, func(t *testing.T) {
			target := fill()
			tc.damage(target)
			_, err := verifyComponent(ctx, &quietLog{}, target, "snap", tc.component)
			var damaged *SnapshotDamagedError
			require.ErrorAs(t, err, &damaged)
			require.Equal(t, "snap", damaged.Snapshot)
		})
	}
}

// A listing's fingerprint ignores order and tells apart any change of key
// or size, also one that keeps the count and the bytes.
func TestBucketListingFingerprint(t *testing.T) {
	of := func(entries ...objectInfo) bucketListing {
		var listing bucketListing
		for _, entry := range entries {
			listing.add(entry.Key, entry.Size)
		}
		return listing
	}
	base := of(objectInfo{"a", 1}, objectInfo{"b/c", 2})
	require.Equal(t, base, of(objectInfo{"b/c", 2}, objectInfo{"a", 1}))
	require.NotEqual(t, base, of(objectInfo{"a", 2}, objectInfo{"b/c", 1}), "sizes swapped between keys")
	require.NotEqual(t, base, of(objectInfo{"a", 1}, objectInfo{"b/d", 2}), "a key renamed")
	require.NotEqual(t, base, of(objectInfo{"a", 1}), "an object missing")
}
