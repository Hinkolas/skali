// Package redact removes secret plaintexts from free-form text before it is
// persisted or displayed. The journal writer runs every log message through
// a Redactor built from the environment's current and candidate secrets, so
// a leaked secret value is replaced by its name instead of stored.
package redact

import (
	"maps"
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

func (r *Redactor) build() {
	pairs := make([]string, 0, 2*len(r.byPlaintext))
	for plaintext, name := range r.byPlaintext {
		if len(plaintext) < minLength {
			continue
		}
		pairs = append(pairs, plaintext, "[redacted:"+name+"]")
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

// Merge returns a new Redactor knowing both sets; on identical plaintexts
// the other Redactor's name wins.
func (r *Redactor) Merge(other *Redactor) *Redactor {
	merged := make(map[string]string, len(r.byPlaintext)+len(other.byPlaintext))
	maps.Copy(merged, r.byPlaintext)
	maps.Copy(merged, other.byPlaintext)
	return New(merged)
}
