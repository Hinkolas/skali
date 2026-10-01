// Package dbcatalog is the dependency-free record of what the managed
// database substrate can provide: the engine majors it has a blessed image
// for and the extensions those images ship. The compiler and the manifest
// schema validate against it offline; the CNPG driver attaches the image
// references to it. Keeping it free of Kubernetes imports is what lets the
// CLI reject an unsupported extension before anything reaches a cluster.
package dbcatalog

import (
	"slices"
	"sort"
)

// postgresExtensions is the extension set every blessed PostgreSQL image
// provides: the curated contrib subset plus pgvector (`vector`), which the
// stock CloudNativePG "system" images package for every supported major.
// Extensions that need shared_preload_libraries (pgaudit, pg_failover_slots)
// are deliberately absent: pools do not expose that setting.
var postgresExtensions = []string{
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
	"vector",
}

// catalog maps engine and major to the extension list its image provides.
// A major present here has a blessed image in the CNPG driver's catalog;
// the two are pinned together by the driver's tests.
var catalog = map[string]map[int][]string{
	"postgres": {
		17: postgresExtensions,
		18: postgresExtensions,
	},
}

// Engines lists the known engines, ascending.
func Engines() []string {
	engines := make([]string, 0, len(catalog))
	for engine := range catalog {
		engines = append(engines, engine)
	}
	sort.Strings(engines)
	return engines
}

// Majors lists the supported majors of an engine, ascending; nil for an
// unknown engine.
func Majors(engine string) []int {
	majors := make([]int, 0, len(catalog[engine]))
	for major := range catalog[engine] {
		majors = append(majors, major)
	}
	sort.Ints(majors)
	return majors
}

// Extensions lists the extensions available on an engine major, sorted;
// nil for an unknown engine or major.
func Extensions(engine string, major int) []string {
	extensions, ok := catalog[engine][major]
	if !ok {
		return nil
	}
	sorted := slices.Clone(extensions)
	sort.Strings(sorted)
	return sorted
}

// AllExtensions lists every extension any engine major provides, sorted
// and deduplicated; the manifest schema offers it as completion.
func AllExtensions() []string {
	var all []string
	for _, majors := range catalog {
		for _, extensions := range majors {
			all = append(all, extensions...)
		}
	}
	sort.Strings(all)
	return slices.Compact(all)
}

// Supports reports whether an engine major provides the named extension.
func Supports(engine string, major int, name string) bool {
	return slices.Contains(catalog[engine][major], name)
}
