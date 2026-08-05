package utils

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, size, err := FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if size != 5 || digest != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Errorf("FileSHA256 = %q with size %d", digest, size)
	}
}

func TestShortChecksum(t *testing.T) {
	cases := map[string]string{
		"sha256:0123456789abcdef0123": "0123456789ab",
		"0123456789abcdef0123":        "0123456789ab",
		"sha256:0123":                 "0123",
		"0123":                        "0123",
	}
	for input, want := range cases {
		if got := ShortChecksum(input); got != want {
			t.Errorf("ShortChecksum(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{
		0:       "0B",
		512:     "512B",
		1 << 10: "1.0KiB",
		1536:    "1.5KiB",
		1 << 20: "1.0MiB",
		1 << 30: "1.0GiB",
		3 << 29: "1.5GiB",
	}
	for input, want := range cases {
		if got := FormatBytes(input); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestSortedKeys(t *testing.T) {
	got := SortedKeys(map[string]int{"b": 1, "a": 2, "c": 3})
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SortedKeys = %v, want %v", got, want)
		}
	}
	if len(SortedKeys(map[string]int(nil))) != 0 {
		t.Error("SortedKeys(nil) should be empty")
	}
}

func TestSameStrings(t *testing.T) {
	left := []string{"b", "a"}
	right := []string{"a", "b"}
	if !SameStrings(left, right) {
		t.Error("expected equal sets")
	}
	if SameStrings([]string{"a"}, []string{"a", "b"}) {
		t.Error("expected unequal sets")
	}
	if left[0] != "b" || right[0] != "a" {
		t.Error("inputs must not be mutated")
	}
}

func TestShortID(t *testing.T) {
	id := uuid.MustParse("018f4b2e-1111-7222-8333-444455556666")
	if got := ShortID(id); got != "55556666" {
		t.Errorf("ShortID = %q, want %q", got, "55556666")
	}
}

func TestNilWhenZero(t *testing.T) {
	if NilWhenZero(uuid.Nil) != nil {
		t.Error("zero UUID must map to nil")
	}
	id := uuid.MustParse("018f4b2e-1111-7222-8333-444455556666")
	if got := NilWhenZero(id); got == nil || *got != id {
		t.Error("non-zero UUID must round-trip")
	}
}

func TestRandomToken(t *testing.T) {
	first, err := RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 43 || first == second {
		t.Errorf("expected two distinct 43-char tokens, got %q and %q", first, second)
	}
}

func TestHumanSince(t *testing.T) {
	if got := HumanSince(time.Now().Add(-30 * time.Second)); got != "30s ago" {
		t.Errorf("HumanSince = %q, want %q", got, "30s ago")
	}
}

func TestPluralCount(t *testing.T) {
	if got := PluralCount(1, "server"); got != "1 server" {
		t.Errorf("PluralCount(1) = %q", got)
	}
	if got := PluralCount(2, "agent"); got != "2 agents" {
		t.Errorf("PluralCount(2) = %q", got)
	}
}
