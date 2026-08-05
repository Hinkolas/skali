package backup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

func newTestTargetStore(t *testing.T) *TargetStore {
	t.Helper()
	pool := testdb.New(t)
	targets, err := NewTargetStore(store.NewStore(pool), strings.Repeat("s", 32))
	require.NoError(t, err)
	return targets
}

func TestTargetStoreRejectsShortSecret(t *testing.T) {
	_, err := NewTargetStore(nil, "short")
	require.Error(t, err)
}

func TestTargetRoundtrip(t *testing.T) {
	targets := newTestTargetStore(t)
	ctx := t.Context()

	_, err := targets.Get(ctx, DefaultTargetName)
	require.ErrorIs(t, err, ErrTargetNotFound)

	input := TargetInput{
		Name:            DefaultTargetName,
		Endpoint:        "https://s3.example.test",
		Region:          "eu-central-1",
		Bucket:          "backups",
		Prefix:          "dev",
		AccessKeyID:     "AKID",
		SecretAccessKey: "super-secret",
	}
	stored, err := targets.Upsert(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "backups", stored.Bucket)

	// Reads carry everything except the secret; credentials decrypt it.
	got, err := targets.Get(ctx, DefaultTargetName)
	require.NoError(t, err)
	require.Equal(t, "AKID", got.AccessKeyID)

	credentials, err := targets.credentials(ctx, DefaultTargetName)
	require.NoError(t, err)
	require.Equal(t, "super-secret", credentials.SecretAccessKey)

	// Upsert replaces in place and rotates the secret.
	input.Bucket = "backups-2"
	input.SecretAccessKey = "rotated"
	_, err = targets.Upsert(ctx, input)
	require.NoError(t, err)
	credentials, err = targets.credentials(ctx, DefaultTargetName)
	require.NoError(t, err)
	require.Equal(t, "backups-2", credentials.Bucket)
	require.Equal(t, "rotated", credentials.SecretAccessKey)
}

func TestTargetMultipleNamesCoexist(t *testing.T) {
	targets := newTestTargetStore(t)
	ctx := t.Context()

	for _, name := range []string{"default", "onsite"} {
		_, err := targets.Upsert(ctx, TargetInput{
			Name: name, Endpoint: "https://" + name + ".example.test",
			Bucket: name, AccessKeyID: "AK", SecretAccessKey: "sk-" + name,
		})
		require.NoError(t, err)
	}
	onsite, err := targets.credentials(ctx, "onsite")
	require.NoError(t, err)
	require.Equal(t, "sk-onsite", onsite.SecretAccessKey)
	fallback, err := targets.credentials(ctx, "default")
	require.NoError(t, err)
	require.Equal(t, "sk-default", fallback.SecretAccessKey)
}

func TestTargetValidation(t *testing.T) {
	targets := newTestTargetStore(t)
	ctx := t.Context()

	for name, input := range map[string]TargetInput{
		"missing endpoint scheme": {Name: "default", Endpoint: "s3.example.test", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"},
		"empty bucket":            {Name: "default", Endpoint: "https://x", AccessKeyID: "a", SecretAccessKey: "s"},
		"empty access key":        {Name: "default", Endpoint: "https://x", Bucket: "b", SecretAccessKey: "s"},
		"empty secret":            {Name: "default", Endpoint: "https://x", Bucket: "b", AccessKeyID: "a"},
		"empty name":              {Endpoint: "https://x", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"},
	} {
		_, err := targets.Upsert(ctx, input)
		var invalid *ValidationError
		require.ErrorAs(t, err, &invalid, name)
	}
}
