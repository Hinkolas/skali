package utils

import "slices"

// SortedKeys returns the map's keys in ascending order for deterministic
// iteration.
func SortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// SameStrings reports whether both slices hold the same elements,
// ignoring order. The inputs are left unmodified.
func SameStrings(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}
