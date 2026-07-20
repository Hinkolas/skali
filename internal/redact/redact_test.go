package redact

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactReplacesSecrets(t *testing.T) {
	t.Parallel()
	r := New(map[string]string{
		"s3cr3t-plant-value": "SESSION_SECRET",
		"abcd1234":           "API_KEY",
	})
	in := "connecting with s3cr3t-plant-value and key abcd1234"
	out := r.Redact(in)
	require.NotContains(t, out, "s3cr3t-plant-value")
	require.NotContains(t, out, "abcd1234")
	require.Contains(t, out, "[redacted:SESSION_SECRET]")
	require.Contains(t, out, "[redacted:API_KEY]")
}

func TestRedactCoversMultipleVersionsOfOneName(t *testing.T) {
	t.Parallel()
	r := New(map[string]string{
		"current-plant-value": "SESSION_SECRET",
		"staged-plant-value":  "SESSION_SECRET",
	})
	out := r.Redact("current-plant-value staged-plant-value")
	require.NotContains(t, out, "current-plant-value")
	require.NotContains(t, out, "staged-plant-value")
	require.Contains(t, out, "[redacted:SESSION_SECRET]")
}

func TestRedactSkipsShortPlaintexts(t *testing.T) {
	t.Parallel()
	r := New(map[string]string{"on": "FLAG", "": "EMPTY"})
	require.Equal(t, "config on and on", r.Redact("config on and on"))
}

func TestMerge(t *testing.T) {
	t.Parallel()
	base := New(map[string]string{"old-plaintext": "TOKEN"})
	next := base.Merge(New(map[string]string{"new-plaintext": "TOKEN"}))
	require.Equal(t, "[redacted:TOKEN] [redacted:TOKEN]", next.Redact("new-plaintext old-plaintext"))
}

func TestNilRedactorPassesThrough(t *testing.T) {
	t.Parallel()
	var r *Redactor
	require.Equal(t, "unchanged", r.Redact("unchanged"))
}
