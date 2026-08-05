package naming

import (
	"regexp"
	"strings"
	"testing"
)

// The exported patterns are what editors enforce through the JSON
// Schemas; the checks are what the product enforces at runtime. This
// test holds the two in exact agreement over an exhaustive corpus of
// short strings plus the length and encoding edges.
func TestChecksAgreeWithPatterns(t *testing.T) {
	corpus := []string{
		"", "é", "aé", strings.Repeat("a", 63), strings.Repeat("a", 64),
		"a" + strings.Repeat("-", 61) + "a", "a" + strings.Repeat("a", 61) + "-",
	}
	alphabet := []string{"a", "9", "-", "A", "_", "."}
	grow := []string{""}
	for range 4 {
		var next []string
		for _, prefix := range grow {
			for _, letter := range alphabet {
				candidate := prefix + letter
				next = append(next, candidate)
				corpus = append(corpus, candidate)
			}
		}
		grow = next
	}

	keyPattern := regexp.MustCompile(KeyPattern)
	envPattern := regexp.MustCompile(EnvNamePattern)
	for _, candidate := range corpus {
		if got, want := CheckKey(candidate) == nil, keyPattern.MatchString(candidate); got != want {
			t.Errorf("CheckKey(%q) accepts=%v, KeyPattern matches=%v", candidate, got, want)
		}
		if got, want := CheckEnvName(candidate) == nil, envPattern.MatchString(candidate); got != want {
			t.Errorf("CheckEnvName(%q) accepts=%v, EnvNamePattern matches=%v", candidate, got, want)
		}
	}
}

func TestCheckKeyMessages(t *testing.T) {
	cases := map[string]string{
		"":      "must not be empty",
		"Web":   "must start with a lowercase letter",
		"1web":  "must start with a lowercase letter",
		"web_1": "must contain only lowercase letters, numbers, and hyphens",
		"web-":  "must not end with a hyphen",
	}
	for input, want := range cases {
		err := CheckKey(input)
		if err == nil || err.Error() != want {
			t.Errorf("CheckKey(%q) = %v, want %q", input, err, want)
		}
	}
	if err := CheckKey(strings.Repeat("a", 64)); err == nil ||
		err.Error() != "is 64 characters long, the maximum is 63" {
		t.Errorf("length message = %v", err)
	}
	for _, valid := range []string{"a", "web", "my-app-2", "a--b"} {
		if err := CheckKey(valid); err != nil {
			t.Errorf("CheckKey(%q) = %v, want nil", valid, err)
		}
	}
}
