package edge

import (
	"fmt"
	"net"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CanonicalDomain returns the exact hostname used for matching and claiming.
// Errors never repeat the input, which may originate in secret values; they
// name at most one offending character and its position, enough to find an
// invisible character or a stray punctuation mark without disclosing the
// value.
func CanonicalDomain(value string) (string, error) {
	for index, c := range value {
		if c > 127 {
			return "", fmt.Errorf("must be an ASCII DNS hostname: %s", describeCharacter(value, index, c))
		}
	}
	value = strings.ToLower(strings.TrimSuffix(value, "."))
	if value == "" {
		return "", fmt.Errorf("must be a DNS hostname: the value is empty")
	}
	if len(value) > 253 {
		return "", fmt.Errorf("must be a DNS hostname: %d characters exceed the 253 character limit", len(value))
	}
	if net.ParseIP(value) != nil {
		return "", fmt.Errorf("must be a DNS hostname, not an IP address")
	}
	offset := 0
	for _, label := range strings.Split(value, ".") {
		switch {
		case len(label) == 0:
			return "", fmt.Errorf("must be a DNS hostname: empty label at position %d", offset+1)
		case len(label) > 63:
			return "", fmt.Errorf("must be a DNS hostname: the label at position %d exceeds 63 characters", offset+1)
		case label[0] == '-' || label[len(label)-1] == '-':
			return "", fmt.Errorf("must be a DNS hostname: the label at position %d starts or ends with a hyphen", offset+1)
		}
		for index, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("must be an ASCII DNS hostname: %s", describeCharacter(value, offset+index, c))
			}
		}
		offset += len(label) + 1
	}
	return value, nil
}

// describeCharacter names one offending character by 1-based character
// position and class. Printable ASCII punctuation is quoted, since a lone
// symbol reveals nothing about the rest of a value; everything else is
// named by code point so invisible characters (a non-breaking space, a
// zero-width space, a carriage return from a CRLF file) become findable.
func describeCharacter(value string, byteIndex int, c rune) string {
	position := utf8.RuneCountInString(value[:byteIndex]) + 1
	switch {
	case c == ' ':
		return fmt.Sprintf("character %d is a space", position)
	case c == '\r':
		return fmt.Sprintf("character %d is a carriage return (CRLF line ending?)", position)
	case c == '\t':
		return fmt.Sprintf("character %d is a tab", position)
	case c == 0xA0:
		return fmt.Sprintf("character %d is a non-breaking space (U+00A0)", position)
	case c < 32 || c == 127:
		return fmt.Sprintf("character %d is a control character (U+%04X)", position, c)
	case c > 127 && unicode.IsSpace(c):
		return fmt.Sprintf("character %d is a non-ASCII space (U+%04X)", position, c)
	case c > 127 && !unicode.IsPrint(c):
		return fmt.Sprintf("character %d is an invisible non-ASCII character (U+%04X)", position, c)
	case c > 127:
		return fmt.Sprintf("character %d is a non-ASCII character (U+%04X)", position, c)
	default:
		return fmt.Sprintf("character %d is %q", position, c)
	}
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
