// Package cnpg is the PostgreSQL engine driver of the database substrate:
// the blessed image catalog and the pure rendering of CNPG Cluster and
// Database objects plus credential Secrets. Rendering is deterministic and
// side-effect free; the substrate controller owns every apply.
package cnpg

import (
	"github.com/Hinkolas/skali/internal/dbcatalog"
)

// Image is one blessed engine image and the extension set it provides. The
// compiler rejects extensions outside this list (through dbcatalog) before a
// revision exists, so a claim never requests what the pool image cannot
// create.
type Image struct {
	Ref        string
	Extensions []string
}

// catalog maps (engine, major) to its blessed image: the stock CNPG "system"
// images, which ship the PostgreSQL contrib set plus pgvector for every
// supported major. The extension list comes from dbcatalog so the compiler,
// the manifest schema, and the driver agree on one record. A pool copies its
// image at creation and keeps it; changing an entry here only affects pools
// created afterwards.
var catalog = map[string]map[int]Image{
	"postgres": {
		17: {Ref: "ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie", Extensions: dbcatalog.Extensions("postgres", 17)},
		18: {Ref: "ghcr.io/cloudnative-pg/postgresql:18.4-system-trixie", Extensions: dbcatalog.Extensions("postgres", 18)},
	},
}

// Lookup returns the blessed image for an engine major.
func Lookup(engine string, major int) (Image, bool) {
	image, ok := catalog[engine][major]
	return image, ok
}

// SupportedMajors lists the catalog's majors for an engine, ascending.
func SupportedMajors(engine string) []int {
	return dbcatalog.Majors(engine)
}
