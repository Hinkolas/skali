package seaweed

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStaleUploadsFromFilerListing: the filer's listing of the upload
// directories (the shape measured on the pin) yields the uploads older
// than the cutoff with the key the S3 abort needs; fresh ones and
// entries without a key are left alone.
func TestStaleUploadsFromFilerListing(t *testing.T) {
	t.Parallel()
	raw := `{"Path":"/buckets/b-files/.uploads","Entries":[
		{"FullPath":"/buckets/b-files/.uploads/abc_1","Crtime":"2026-09-30T10:00:00Z","Extended":{"key":"ZGlyL3N0YWxl","Seaweed-X-Amz-Owner":"YWRtaW4="}},
		{"FullPath":"/buckets/b-files/.uploads/abc_2","Crtime":"2026-09-30T11:59:00Z","Extended":{"key":"ZnJlc2g="}},
		{"FullPath":"/buckets/b-files/.uploads/abc_3","Crtime":"2026-09-30T09:00:00Z","Extended":{}}
	],"LastFileName":"abc_3","ShouldDisplayLoadMore":false}`
	var listing uploadListing
	require.NoError(t, json.Unmarshal([]byte(raw), &listing))
	cutoff := time.Date(2026, 9, 30, 11, 0, 0, 0, time.UTC)
	require.Equal(t, []staleUpload{{ID: "abc_1", Key: "dir/stale"}}, staleUploads(listing.Entries, cutoff))
	require.Equal(t, "abc_3", listing.LastFileName)
	require.False(t, listing.ShouldDisplayLoadMore)
}
