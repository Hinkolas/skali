// Package redact removes secret plaintexts from free-form text before it is
// persisted or displayed. The journal writer runs every log message through
// a Redactor built from the environment's current and candidate secrets, so
// a leaked secret value is replaced by its name instead of stored.
package redact

import (
	"maps"
	"slices"
	"strings"
)

// minLength guards against mangling text: plaintexts shorter than this are
// not matched, because 1-3 character fragments appear in ordinary output far
// too often to replace safely.
const minLength = 4

type Redactor struct {
	// byPlaintext maps plaintext -> secret name. Keying by plaintext lets
	// several versions of the same named secret be redacted at once.
	byPlaintext map[string]string
	replacer    *strings.Replacer
}

// New builds a Redactor over plaintext -> name pairs. Empty and very short
// plaintexts are kept for Merge but never matched.
func New(byPlaintext map[string]string) *Redactor {
	r := &Redactor{byPlaintext: make(map[string]string, len(byPlaintext))}
	maps.Copy(r.byPlaintext, byPlaintext)
	r.build()
	return r
}

// build orders the plaintexts longest first: at one position the replacer
// takes the first pair that matches, so a secret that is a prefix of
// another (a retired version of a rotated value) must not cut the longer
// one short and leave its rest in the text.
func (r *Redactor) build() {
	plaintexts := make([]string, 0, len(r.byPlaintext))
	for plaintext := range r.byPlaintext {
		if len(plaintext) >= minLength {
			plaintexts = append(plaintexts, plaintext)
		}
	}
	slices.SortFunc(plaintexts, func(a, b string) int {
		if diff := len(b) - len(a); diff != 0 {
			return diff
		}
		return strings.Compare(a, b)
	})
	pairs := make([]string, 0, 2*len(plaintexts))
	for _, plaintext := range plaintexts {
		pairs = append(pairs, plaintext, "[redacted:"+r.byPlaintext[plaintext]+"]")
	}
	r.replacer = strings.NewReplacer(pairs...)
}

// Redact replaces every known secret plaintext with [redacted:NAME].
func (r *Redactor) Redact(s string) string {
	if r == nil || r.replacer == nil {
		return s
	}
	return r.replacer.Replace(s)
}

// Without returns a new Redactor that no longer matches the plaintexts of
// the named secrets. It exists for values that are public by construction,
// such as a route domain the edge serves to the world: redacting those
// would hide the one fact an operator needs to read.
func (r *Redactor) Without(names map[string]bool) *Redactor {
	if r == nil || len(names) == 0 {
		return r
	}
	kept := make(map[string]string, len(r.byPlaintext))
	for plaintext, name := range r.byPlaintext {
		if !names[name] {
			kept[plaintext] = name
		}
	}
	return New(kept)
}

// Merge returns a new Redactor knowing both sets; on identical plaintexts
// the other Redactor's name wins.
func (r *Redactor) Merge(other *Redactor) *Redactor {
	merged := make(map[string]string, len(r.byPlaintext)+len(other.byPlaintext))
	maps.Copy(merged, r.byPlaintext)
	maps.Copy(merged, other.byPlaintext)
	return New(merged)
}
