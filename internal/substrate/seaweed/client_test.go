package seaweed

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeDoer answers ServiceProxyDo with a canned body; exec is unused here.
type fakeDoer struct {
	body   []byte
	status int
}

func (f *fakeDoer) ServiceProxyDo(_ context.Context, _, _, _ string, _ int, _ string, _ url.Values, _ []byte) ([]byte, int, error) {
	return f.body, f.status, nil
}

func (f *fakeDoer) ExecInPod(context.Context, string, string, string, []string) (string, error) {
	return "", nil
}

// The fixture mirrors the master /vol/status shape: two volume servers, one
// bucket volume replicated on both (same id), plus a default-collection
// volume on one. VolumeSizesByNode must charge each replica to its server;
// CollectionSizes must dedupe by id and skip the default collection.
const volStatusFixture = `{
  "Volumes": {
    "DataCenters": {
      "DefaultDataCenter": {
        "DefaultRack": {
          "10.42.0.80:8080": [
            {"Id": 1, "Collection": "bucket-a", "Size": 1000, "FileCount": 3},
            {"Id": 7, "Collection": "", "Size": 50, "FileCount": 1}
          ],
          "10.42.1.12:8080": [
            {"Id": 1, "Collection": "bucket-a", "Size": 1000, "FileCount": 3},
            {"Id": 2, "Collection": "bucket-b", "Size": 200, "FileCount": 2}
          ]
        }
      }
    }
  }
}`

func TestVolumeSizesByNode(t *testing.T) {
	t.Parallel()
	client := NewClient(&fakeDoer{body: []byte(volStatusFixture), status: 200}, "skali-platform")
	sizes, err := client.VolumeSizesByNode(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]int64{
		"10.42.0.80:8080": 1050,
		"10.42.1.12:8080": 1200,
	}, sizes, "replicas count on every server and the default collection is disk too")
}

func TestCollectionSizesDedupes(t *testing.T) {
	t.Parallel()
	client := NewClient(&fakeDoer{body: []byte(volStatusFixture), status: 200}, "skali-platform")
	stats, err := client.CollectionSizes(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1000), stats["bucket-a"].SizeBytes, "replicas must count once for quota")
	require.Equal(t, int64(200), stats["bucket-b"].SizeBytes)
	_, ok := stats[""]
	require.False(t, ok, "the default collection is not a bucket")
}
