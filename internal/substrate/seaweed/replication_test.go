package seaweed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// confDoer keeps filer.conf in memory (absent until the first PUT),
// records every PUT body and every shell script, and answers execs with a
// canned transcript.
type confDoer struct {
	fakeDoer
	conf    []byte
	puts    [][]byte
	scripts []string
	output  string
}

func (d *confDoer) ServiceProxyDo(_ context.Context, method, _, _ string, _ int, path string, _ url.Values, body []byte) ([]byte, int, error) {
	if path != FilerConfPath {
		return nil, http.StatusNotFound, nil
	}
	switch method {
	case http.MethodGet:
		if d.conf == nil {
			return nil, http.StatusNotFound, nil
		}
		return d.conf, http.StatusOK, nil
	case http.MethodPut:
		d.conf = append([]byte(nil), body...)
		d.puts = append(d.puts, d.conf)
		return nil, http.StatusCreated, nil
	}
	return nil, http.StatusMethodNotAllowed, nil
}

func (d *confDoer) ExecInPod(_ context.Context, _, _, _ string, command []string) (string, error) {
	d.scripts = append(d.scripts, command[len(command)-1])
	return d.output, nil
}

// TestEnsureReplicationPathWritesOnce: the first call creates the buckets
// entry and reports a change, a matching document is left untouched, a
// different code is rewritten, and unrelated entries (a quota flip)
// survive every write.
func TestEnsureReplicationPathWritesOnce(t *testing.T) {
	t.Parallel()
	quota := PathConf{LocationPrefix: BucketsPrefix + "b-files-1/", ReadOnly: true}
	seed, err := json.Marshal(FilerConf{Locations: []PathConf{quota}})
	require.NoError(t, err)
	doer := &confDoer{conf: seed}
	client := NewClient(doer, "skali-platform")
	ctx := context.Background()

	changed, err := client.EnsureReplicationPath(ctx, "001")
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, doer.puts, 1)
	var conf FilerConf
	require.NoError(t, json.Unmarshal(doer.puts[0], &conf))
	require.Equal(t, []PathConf{quota, {LocationPrefix: BucketsPrefix, Replication: "001"}}, conf.Locations)

	changed, err = client.EnsureReplicationPath(ctx, "001")
	require.NoError(t, err)
	require.False(t, changed)
	require.Len(t, doer.puts, 1, "a matching entry is settled")

	changed, err = client.EnsureReplicationPath(ctx, "010")
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, doer.puts, 2)
	require.NoError(t, json.Unmarshal(doer.puts[1], &conf))
	require.Equal(t, []PathConf{quota, {LocationPrefix: BucketsPrefix, Replication: "010"}}, conf.Locations)
}

// TestConfigureVolumeReplicationLocks: the command runs under the shell's
// maintenance lock, an "error:" line in the transcript is an error even
// though the shell exits zero, and a client without a filer target cannot
// run it at all.
func TestConfigureVolumeReplicationLocks(t *testing.T) {
	t.Parallel()
	doer := &confDoer{}
	client := NewClient(doer, "skali-platform")
	ctx := context.Background()
	require.Error(t, client.ConfigureVolumeReplication(ctx, "001"), "no filer target yet")
	require.Empty(t, doer.scripts)

	client.SetFilerTarget("app=seaweed-filer", "filer")
	require.NoError(t, client.ConfigureVolumeReplication(ctx, "001"))
	require.Len(t, doer.scripts, 1)
	require.True(t, strings.HasPrefix(doer.scripts[0],
		"echo 'lock\nvolume.configure.replication -replication=001 -collectionPattern=*\nunlock' | weed shell"),
		doer.scripts[0])

	doer.output = "> lock\n> volume.configure.replication\nerror: need to run \"lock\" first\n"
	err := client.ConfigureVolumeReplication(ctx, "001")
	require.Error(t, err)
	require.Contains(t, err.Error(), `error: need to run "lock" first`)
	require.False(t, errors.Is(err, context.DeadlineExceeded))
}
