// Package utils holds small generic helpers shared across the codebase.
// Keep it dependency-light: standard library plus google/uuid only, so
// importing it never drags server-side dependencies into the CLI.
package utils

import "os"

// AnySlice widens values into []any, e.g. for jsonschema enum lists.
func AnySlice[T any](values ...T) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

// EnvOr returns the environment variable's value, or fallback when the
// variable is unset or empty.
func EnvOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
