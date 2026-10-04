package backup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObjectListingUsesSmallPagesAndFollowsContinuation(t *testing.T) {
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("max-keys") != "100" {
			// Model a gateway that cannot finish its default large page
			// before the client's response-header deadline.
			http.Error(w, "listing page too large", http.StatusBadRequest)
			return
		}
		if q.Get("prefix") != "folder/" || q.Get("delimiter") != "" || q.Get("list-type") != "2" {
			http.Error(w, "unexpected listing request", http.StatusBadRequest)
			return
		}
		token := q.Get("continuation-token")
		tokens = append(tokens, token)
		page := len(tokens)
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>%t</IsTruncated><NextContinuationToken>page-%d</NextContinuationToken><Contents><Key>folder/object-%d</Key><Size>%d</Size></Contents></ListBucketResult>`, page < 3, page, page, page*10)
	}))
	defer server.Close()
	source, err := newObjectStore(s3Location{Endpoint: server.URL, Region: "us-east-1", Bucket: "bucket"})
	require.NoError(t, err)
	var objects []objectInfo
	require.NoError(t, source.List(context.Background(), "folder/", func(info objectInfo) error {
		objects = append(objects, info)
		return nil
	}))
	require.Equal(t, []string{"", "page-1", "page-2"}, tokens)
	require.Equal(t, []objectInfo{{Key: "folder/object-1", Size: 10}, {Key: "folder/object-2", Size: 20}, {Key: "folder/object-3", Size: 30}}, objects)
}
