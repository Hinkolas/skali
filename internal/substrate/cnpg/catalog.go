// Package cnpg is the PostgreSQL engine driver of the database substrate:
// the blessed image catalog and the pure rendering of CNPG Cluster and
// Database objects plus credential Secrets. Rendering is deterministic and
// side-effect free; the substrate controller owns every apply.
package cnpg

import (
	"slices"
	"sort"
)

// Image is one blessed engine image and the extension set it provides.
// Extensions outside this list are rejected at deploy open, so a claim can
// never request what the pool image cannot create.
type Image struct {
	Ref        string
	Extensions []string
}

// catalog maps (engine, major) to its blessed image. R5 pins the stock CNPG
// "system" images; a skali-built image with pgvector/postgis replaces an
// entry here without any schema or claim change. The extension list is the
// curated contrib subset those images ship.
var catalog = map[string]map[int]Image{
	"postgres": {
		17: {Ref: "ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie", Extensions: contribExtensions},
		18: {Ref: "ghcr.io/cloudnative-pg/postgresql:18.4-system-trixie", Extensions: contribExtensions},
	},
}

var contribExtensions = []string{
	"btree_gin",
	"btree_gist",
	"citext",
	"cube",
	"earthdistance",
	"fuzzystrmatch",
	"hstore",
	"intarray",
	"ltree",
	"pg_stat_statements",
	"pg_trgm",
	"pgcrypto",
	"tablefunc",
	"unaccent",
	"uuid-ossp",
}

// Lookup returns the blessed image for an engine major.
func Lookup(engine string, major int) (Image, bool) {
	image, ok := catalog[engine][major]
	return image, ok
}

// SupportedMajors lists the catalog's majors for an engine, ascending.
func SupportedMajors(engine string) []int {
	majors := make([]int, 0, len(catalog[engine]))
	for major := range catalog[engine] {
		majors = append(majors, major)
	}
	sort.Ints(majors)
	return majors
}

// SupportsExtension reports whether the image provides an extension.
func (i Image) SupportsExtension(name string) bool {
	return slices.Contains(i.Extensions, name)
}
