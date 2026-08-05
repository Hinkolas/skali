package utils

import (
	"strings"

	"github.com/google/uuid"
)

// NilWhenZero maps the zero UUID to nil so optional references write
// NULL instead of the zero value.
func NilWhenZero(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// ShortID is the collision-resistant fragment of generated identities.
// It takes the LAST 8 hex characters: a v7 UUID's leading characters are
// pure timestamp, identical for every id minted in the same window, so
// two ids created in one pass would collide on a prefix fragment.
func ShortID(id uuid.UUID) string {
	compact := strings.ReplaceAll(id.String(), "-", "")
	return compact[len(compact)-8:]
}
