// Package naming is the authority on the product's identifier grammar:
// the stable keys and names shared by manifests, layouts, projects, and
// environments, and the environment variable names values may use. The
// hand-written checks are authoritative and name the specific violation;
// the exported pattern constants exist for the JSON Schemas editors
// consume, and the package tests hold each pattern and its check in
// exact agreement.
package naming

import (
	"errors"
	"fmt"
)

// KeyPattern is CheckKey as a JSON Schema pattern: lowercase letters,
// numbers, and inner hyphens, starting with a letter and ending
// alphanumeric, at most 63 characters. Every key is a DNS-1123 label,
// so it can serve as a Kubernetes object name, container name, or
// label value without repair.
const KeyPattern = "^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$"

// CheckKey validates a stable key or name against KeyPattern. The
// message composes after a noun: "key must not end with a hyphen".
func CheckKey(value string) error {
	if value == "" {
		return errors.New("must not be empty")
	}
	if len(value) > 63 {
		return fmt.Errorf("is %d characters long, the maximum is 63", len(value))
	}
	if value[0] < 'a' || value[0] > 'z' {
		return errors.New("must start with a lowercase letter")
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') || c == '-' {
			continue
		}
		return errors.New("must contain only lowercase letters, numbers, and hyphens")
	}
	if value[len(value)-1] == '-' {
		return errors.New("must not end with a hyphen")
	}
	return nil
}

// EnvNamePattern is CheckEnvName as a JSON Schema pattern: a POSIX
// style environment variable name.
const EnvNamePattern = "^[A-Za-z_][A-Za-z0-9_]*$"

// CheckEnvName validates an environment variable name against
// EnvNamePattern.
func CheckEnvName(value string) error {
	if value == "" {
		return errors.New("must not be empty")
	}
	if '0' <= value[0] && value[0] <= '9' {
		return errors.New("must start with a letter or underscore")
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') || c == '_' {
			continue
		}
		return errors.New("must contain only letters, numbers, and underscores")
	}
	return nil
}
