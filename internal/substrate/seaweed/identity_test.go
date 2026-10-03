package seaweed

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingDoer answers the identity print with a canned document and
// records every other shell script.
type recordingDoer struct {
	fakeDoer
	identities IdentityConfig
	scripts    []string
}

func (r *recordingDoer) ExecInPod(_ context.Context, _, _, _ string, command []string) (string, error) {
	script := command[len(command)-1]
	if strings.HasPrefix(script, "echo 's3.configure' |") {
		data, _ := json.Marshal(r.identities)
		return "> " + string(data) + "\n", nil
	}
	r.scripts = append(r.scripts, script)
	return "", nil
}

func TestEnsureIdentitySettledIgnoresActionOrder(t *testing.T) {
	t.Parallel()
	doer := &recordingDoer{identities: IdentityConfig{Identities: []Identity{{
		Name:        "b-files-1",
		Credentials: []Credential{{AccessKey: "AK", SecretKey: "SK"}},
		Actions:     []string{"Write:b-files-1", "Read:b-files-1", "Tagging:b-files-1", "List:b-files-1"},
	}}}}
	client := NewClient(doer, "skali-platform")
	client.SetFilerTarget("app=seaweed", "seaweed")
	require.NoError(t, client.EnsureIdentity(context.Background(), Identity{
		Name:        "b-files-1",
		Credentials: []Credential{{AccessKey: "AK", SecretKey: "SK"}},
		Actions:     BucketActions("b-files-1"),
	}))
	require.Empty(t, doer.scripts, "the engine reports actions in its own order; that is settled")
}

func TestEnsureIdentityRemovesStaleActions(t *testing.T) {
	t.Parallel()
	doer := &recordingDoer{identities: IdentityConfig{Identities: []Identity{{
		Name:        "b-files-1",
		Credentials: []Credential{{AccessKey: "AK", SecretKey: "SK"}},
		Actions:     []string{"Read:b-files-1", "Write:b-files-1", "Admin"},
	}}}}
	client := NewClient(doer, "skali-platform")
	client.SetFilerTarget("app=seaweed", "seaweed")
	require.NoError(t, client.EnsureIdentity(context.Background(), Identity{
		Name:        "b-files-1",
		Credentials: []Credential{{AccessKey: "AK", SecretKey: "SK"}},
		Actions:     []string{"Read:b-files-1"},
	}))
	require.Len(t, doer.scripts, 1)
	// s3.configure only ever appends: the keypair line re-asserts the
	// wanted set, then the stale actions are deleted explicitly.
	require.Contains(t, doer.scripts[0],
		"s3.configure -user=b-files-1 -access_key=AK -secret_key=SK -actions=Read:b-files-1 -apply")
	require.Contains(t, doer.scripts[0],
		"s3.configure -user=b-files-1 -actions=Write:b-files-1,Admin -delete -apply")
}

func TestBucketPolicyDeniesAdministrationForTheBucketIdentity(t *testing.T) {
	t.Parallel()
	var document struct {
		Version   string
		Statement []struct {
			Effect    string
			Principal map[string]string
			Action    []string
			Resource  []string
		}
	}
	require.NoError(t, json.Unmarshal([]byte(BucketPolicy("b-files-1")), &document))
	require.Equal(t, "2012-10-17", document.Version)
	require.Len(t, document.Statement, 1)
	statement := document.Statement[0]
	require.Equal(t, "Deny", statement.Effect)
	require.Equal(t, PrincipalARN("b-files-1"), statement.Principal["AWS"])
	require.Equal(t, []string{"arn:aws:s3:::b-files-1", "arn:aws:s3:::b-files-1/*"}, statement.Resource)
	for _, action := range []string{"s3:PutBucketPolicy", "s3:DeleteBucketPolicy", "s3:PutBucketCors",
		"s3:PutLifecycleConfiguration", "s3:PutBucketVersioning", "s3:PutBucketAcl", "s3:PutObjectAcl"} {
		require.Contains(t, statement.Action, action)
	}
	require.NotContains(t, statement.Action, "s3:PutObject", "object access is the identity's, never denied")
	require.NotContains(t, statement.Action, "s3:GetObject")
}

func TestSamePolicyIsStructural(t *testing.T) {
	t.Parallel()
	require.True(t, samePolicy(`{"Version":"2012-10-17", "Statement": []}`, `{"Statement":[],"Version":"2012-10-17"}`))
	require.False(t, samePolicy("", `{"Version":"2012-10-17"}`))
	require.True(t, samePolicy("", ""))
	require.False(t, samePolicy(`{"Version":"1"}`, `{"Version":"2012-10-17"}`))
}

// A rotation's overlap adds the new keypair beside the old one without
// touching it; retiring is the explicit delete of exactly the dropped key.
// Settled ignores the order the engine lists keypairs in.
func TestEnsureIdentityRotatesKeypairs(t *testing.T) {
	t.Parallel()
	actions := BucketActions("b-files-1")
	doer := &recordingDoer{identities: IdentityConfig{Identities: []Identity{{
		Name:        "b-files-1",
		Credentials: []Credential{{AccessKey: "AKOLD", SecretKey: "SKOLD"}},
		Actions:     actions,
	}}}}
	client := NewClient(doer, "skali-platform")
	client.SetFilerTarget("app=seaweed", "seaweed")

	// Add: the overlap lists the current pair first.
	require.NoError(t, client.EnsureIdentity(context.Background(), Identity{
		Name:        "b-files-1",
		Credentials: []Credential{{AccessKey: "AKNEW", SecretKey: "SKNEW"}, {AccessKey: "AKOLD", SecretKey: "SKOLD"}},
		Actions:     actions,
	}))
	require.Len(t, doer.scripts, 1)
	require.Contains(t, doer.scripts[0], "-access_key=AKNEW -secret_key=SKNEW")
	require.Contains(t, doer.scripts[0], "-access_key=AKOLD -secret_key=SKOLD")
	require.NotContains(t, doer.scripts[0], "-delete", "the overlap keeps the old key")

	// Settled: the engine appended the new pair after the old one.
	doer.identities.Identities[0].Credentials = []Credential{
		{AccessKey: "AKOLD", SecretKey: "SKOLD"}, {AccessKey: "AKNEW", SecretKey: "SKNEW"}}
	doer.scripts = nil
	require.NoError(t, client.EnsureIdentity(context.Background(), Identity{
		Name:        "b-files-1",
		Credentials: []Credential{{AccessKey: "AKNEW", SecretKey: "SKNEW"}, {AccessKey: "AKOLD", SecretKey: "SKOLD"}},
		Actions:     actions,
	}))
	require.Empty(t, doer.scripts, "keypair order is the engine's business")

	// Retire: exactly the dropped key is deleted.
	require.NoError(t, client.EnsureIdentity(context.Background(), Identity{
		Name:        "b-files-1",
		Credentials: []Credential{{AccessKey: "AKNEW", SecretKey: "SKNEW"}},
		Actions:     actions,
	}))
	require.Len(t, doer.scripts, 1)
	require.Contains(t, doer.scripts[0], "s3.configure -user=b-files-1 -access_key=AKOLD -delete -apply")
	require.NotContains(t, doer.scripts[0], "-access_key=AKNEW -delete")
}
