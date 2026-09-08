package edge

import (
	"fmt"
	"net"
	"strings"
	"unicode/utf8"
)

// CanonicalDomain returns the exact hostname used for matching and claiming.
// Errors deliberately exclude input, which may originate in secret values.
func CanonicalDomain(value string) (string, error) {
	for _, c := range value {
		if c > 127 {
			return "", fmt.Errorf("must be an ASCII DNS hostname")
		}
	}
	value = strings.ToLower(strings.TrimSuffix(value, "."))
	if value == "" || len(value) > 253 || net.ParseIP(value) != nil {
		return "", fmt.Errorf("must be a DNS hostname")
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("must be a DNS hostname")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("must be an ASCII DNS hostname")
			}
		}
	}
	return value, nil
}

func ValidatePath(value string) error {
	if !strings.HasPrefix(value, "/") || !utf8.ValidString(value) {
		return fmt.Errorf("must be a UTF-8 path starting with /")
	}
	for _, c := range value {
		if c < 32 || c == 127 || c == '?' || c == '#' {
			return fmt.Errorf("must not contain control characters, a query, or a fragment")
		}
	}
	return nil
}
