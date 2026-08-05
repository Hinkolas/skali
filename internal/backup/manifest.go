package backup

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	// ManifestFormatVersion is the snapshot manifest format. Readers accept
	// exactly this value and reject anything else loudly, mirroring the
	// manifest schema-version policy.
	ManifestFormatVersion = "1"
	// EncryptionNone records that component objects are stored as written.
	// The field exists from day one so client-side encryption can layer on
	// later without a format break.
	EncryptionNone = "none"
)

// Component kinds inside a snapshot manifest.
const (
	ComponentDatabase = "database"
	ComponentBucket   = "bucket"
	ComponentVolume   = "volume"
)

// Manifest is one complete snapshot: the identity of what was backed up,
// the revision it was running, and every component's storage location. It
// is written to S3 last, after all components succeeded, so a manifest's
// existence certifies a complete snapshot. It deliberately contains no
// secrets and no values: redeploying supplies configuration, the snapshot
// supplies data.
type Manifest struct {
	FormatVersion    string          `json:"format_version"`
	SnapshotID       string          `json:"snapshot_id"`
	Encryption       string          `json:"encryption"`
	CreatedAt        time.Time       `json:"created_at"`
	SkaliVersion     string          `json:"skali_version"`
	Project          string          `json:"project"`
	Environment      string          `json:"environment"`
	RevisionChecksum string          `json:"revision_checksum"`
	Revision         json.RawMessage `json:"revision"`
	Components       []Component     `json:"components"`
}

// Component is one backed-up data unit. Databases and buckets carry their
// service key; volumes carry the application key and volume name. A
// database or volume stores one object (ObjectKey); a bucket stores its
// objects under ObjectPrefix.
type Component struct {
	Kind         string `json:"kind"`
	ServiceKey   string `json:"service_key,omitempty"`
	Application  string `json:"application,omitempty"`
	Volume       string `json:"volume,omitempty"`
	ObjectKey    string `json:"object_key,omitempty"`
	ObjectPrefix string `json:"object_prefix,omitempty"`
	ObjectCount  int64  `json:"object_count,omitempty"`
	Bytes        int64  `json:"bytes"`
	SHA256       string `json:"sha256,omitempty"`
	Status       string `json:"status"`
}

// UnsupportedManifestError reports a manifest whose format this build does
// not read.
type UnsupportedManifestError struct {
	Found string
}

func (e *UnsupportedManifestError) Error() string {
	return fmt.Sprintf("backup: unsupported snapshot manifest format %q (this build reads %q)",
		e.Found, ManifestFormatVersion)
}

func encodeManifest(m *Manifest) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("backup: encode manifest: %w", err)
	}
	return append(data, '\n'), nil
}

func decodeManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("backup: decode manifest: %w", err)
	}
	if m.FormatVersion != ManifestFormatVersion {
		return nil, &UnsupportedManifestError{Found: m.FormatVersion}
	}
	return &m, nil
}

// componentLabel names a component for step keys and messages.
func componentLabel(c Component) string {
	switch c.Kind {
	case ComponentVolume:
		return fmt.Sprintf("volume:%s.%s", c.Application, c.Volume)
	case ComponentBucket:
		return "bucket:" + c.ServiceKey
	default:
		return "db:" + c.ServiceKey
	}
}
