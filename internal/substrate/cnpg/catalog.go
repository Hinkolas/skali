// Package cnpg is the PostgreSQL engine driver of the database substrate:
// the blessed image catalog and the pure rendering of CNPG Cluster and
// Database objects plus credential Secrets. Rendering is deterministic and
// side-effect free; the substrate controller owns every apply.
package cnpg

import (
	"slices"

	"github.com/Hinkolas/skali/internal/dbcatalog"
)

// Image is one blessed engine image and the extension set it provides. The
// compiler rejects extensions outside this list (through dbcatalog) before a
// revision exists, so a claim never requests what the pool image cannot
// create.
type Image struct {
	Ref        string
	Extensions []string
	// PatchesFrom lists the earlier images of the same major whose pools
	// move to Ref on their own, one pool at a time (see the substrate's
	// patchPool). Each entry is a reviewed transition, not a version
	// range: the same PostgreSQL major, OS base and collation libraries,
	// and no step a pool cannot take while running. A pool on an image
	// missing here keeps it until a release adds the transition.
	PatchesFrom []string
}

// Patches reports whether a pool running ref moves to this image.
func (i Image) Patches(ref string) bool {
	return slices.Contains(i.PatchesFrom, ref)
}

// catalog maps (engine, major) to its blessed image: the stock CNPG "system"
// images, which ship the PostgreSQL contrib set plus pgvector for every
// supported major. The extension list comes from dbcatalog so the compiler,
// the manifest schema, and the driver agree on one record. A pool copies its
// image at creation and keeps it; changing a Ref creates new pools on the
// new image and moves existing ones only from the images its PatchesFrom
// lists. docs/postgres-patch-updates.md is the review a transition needs.
var catalog = map[string]map[int]Image{
	"postgres": {
		17: {
			Ref:         "ghcr.io/cloudnative-pg/postgresql:17.11-system-trixie",
			Extensions:  dbcatalog.Extensions("postgres", 17),
			PatchesFrom: []string{"ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie"},
		},
		18: {
			Ref:         "ghcr.io/cloudnative-pg/postgresql:18.6-system-trixie",
			Extensions:  dbcatalog.Extensions("postgres", 18),
			PatchesFrom: []string{"ghcr.io/cloudnative-pg/postgresql:18.4-system-trixie"},
		},
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
