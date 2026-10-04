package backup

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// treeServer is an S3 bucket in memory that answers ListObjectsV2 the way
// a gateway does, delimiter and continuation included, and bulk deletes.
// Every listing takes a moment so concurrent listings overlap, and the
// server records how many were in flight at once. A listing without a
// delimiter fails the test: a walking store never asks for one.
type treeServer struct {
	t       *testing.T
	mu      sync.Mutex
	objects map[string]int64
	lists   int
	active  atomic.Int32
	peak    atomic.Int32
}

func newTreeServer(t *testing.T, objects map[string]int64) (*treeServer, *minioStore) {
	t.Helper()
	tree := &treeServer{t: t, objects: objects}
	server := httptest.NewServer(tree)
	t.Cleanup(server.Close)
	store, err := newObjectStore(s3Location{Endpoint: server.URL, Region: "us-east-1", Bucket: "bucket"})
	require.NoError(t, err)
	store.walk = true
	return tree, store
}

func (s *treeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if r.Method == http.MethodPost && q.Has("delete") {
		s.delete(w, r)
		return
	}
	if q.Get("list-type") != "2" {
		http.Error(w, "unexpected request", http.StatusBadRequest)
		return
	}
	if q.Get("delimiter") != "/" {
		s.t.Errorf("recursive listing of %q", q.Get("prefix"))
		http.Error(w, "recursive listing", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.lists++
	s.mu.Unlock()
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for peak := s.peak.Load(); active > peak && !s.peak.CompareAndSwap(peak, active); peak = s.peak.Load() {
	}
	time.Sleep(5 * time.Millisecond)

	prefix, after := q.Get("prefix"), q.Get("continuation-token")
	limit := 1000
	if raw := q.Get("max-keys"); raw != "" {
		limit, _ = strconv.Atoi(raw)
	}
	s.mu.Lock()
	entries := map[string]int64{} // key → size; common prefixes are -1
	for key, size := range s.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if i := strings.Index(key[len(prefix):], "/"); i >= 0 {
			entries[key[:len(prefix)+i+1]] = -1
		} else {
			entries[key] = size
		}
	}
	s.mu.Unlock()
	keys := make([]string, 0, len(entries))
	for key := range entries {
		if key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	truncated := len(keys) > limit
	if truncated {
		keys = keys[:limit]
	}
	var body strings.Builder
	fmt.Fprintf(&body, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>bucket</Name><Prefix>%s</Prefix><Delimiter>/</Delimiter><KeyCount>%d</KeyCount><MaxKeys>%d</MaxKeys><IsTruncated>%t</IsTruncated>`,
		escapeXML(prefix), len(keys), limit, truncated)
	if truncated {
		fmt.Fprintf(&body, `<NextContinuationToken>%s</NextContinuationToken>`, escapeXML(keys[len(keys)-1]))
	}
	for _, key := range keys {
		if entries[key] < 0 {
			fmt.Fprintf(&body, `<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>`, escapeXML(key))
		} else {
			fmt.Fprintf(&body, `<Contents><Key>%s</Key><Size>%d</Size></Contents>`, escapeXML(key), entries[key])
		}
	}
	body.WriteString(`</ListBucketResult>`)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, body.String())
}

func (s *treeServer) delete(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Objects []struct{ Key string } `xml:"Object"`
	}
	if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	for _, object := range request.Objects {
		delete(s.objects, object.Key)
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`)
}

func (s *treeServer) listings() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lists
}

func escapeXML(value string) string {
	var out strings.Builder
	_ = xml.EscapeText(&out, []byte(value))
	return out.String()
}

// uploadsTree is the layout that made recursive listings slow: one
// directory per object, plus a directory marker, an object at the root,
// and one directory too large for a single listing page.
func uploadsTree() map[string]int64 {
	objects := map[string]int64{"readme.txt": 7, "docs/": 0, "docs/guide.pdf": 11}
	for i := range 60 {
		objects[fmt.Sprintf("uploads/%03d/file.jpg", i)] = int64(i)
	}
	for i := range 1500 {
		objects[fmt.Sprintf("flat/%04d", i)] = 1
	}
	return objects
}

// An in-cluster bucket lists as a tree of level listings, several at
// once, and yields exactly the objects a recursive listing would: the
// directory marker included, directories themselves not, a level larger
// than one page complete.
func TestWalkingStoreListsEveryObjectLevelByLevel(t *testing.T) {
	t.Parallel()
	want := uploadsTree()
	tree, store := newTreeServer(t, maps.Clone(want))

	got := map[string]int64{}
	var inFn atomic.Bool
	require.NoError(t, store.List(context.Background(), "", func(info objectInfo) error {
		require.True(t, inFn.CompareAndSwap(false, true), "fn is called one at a time")
		defer inFn.Store(false)
		_, seen := got[info.Key]
		require.False(t, seen, "%s listed twice", info.Key)
		got[info.Key] = info.Size
		return nil
	}))
	require.Equal(t, want, got)
	require.Greater(t, tree.peak.Load(), int32(1), "levels are listed concurrently")

	got = map[string]int64{}
	require.NoError(t, store.List(context.Background(), "uploads/00", func(info objectInfo) error {
		got[info.Key] = info.Size
		return nil
	}))
	require.Len(t, got, 10, "a prefix that is not a directory still matches by key")
}

// An fn error stops the walk: List returns it, the listings already on
// the wire are the last ones, and the rest of the tree is never listed.
func TestWalkingStoreStopsOnCallbackError(t *testing.T) {
	t.Parallel()
	tree, store := newTreeServer(t, uploadsTree())
	stop := errors.New("stop")
	calls := 0
	err := store.List(context.Background(), "", func(objectInfo) error {
		calls++
		if calls == 5 {
			return stop
		}
		return nil
	})
	require.ErrorIs(t, err, stop)
	require.Equal(t, 5, calls)
	time.Sleep(50 * time.Millisecond) // requests already sent may still arrive
	listed := tree.listings()
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, listed, tree.listings(), "the walk is not running on in the background")
	require.Less(t, listed, 60, "the walk stopped long before covering the tree")
}

// The restore's clear of an in-cluster bucket walks it too.
func TestWalkingStoreRemovesEveryObject(t *testing.T) {
	t.Parallel()
	tree, store := newTreeServer(t, uploadsTree())
	removed, err := store.RemoveAll(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, len(uploadsTree()), removed)
	tree.mu.Lock()
	defer tree.mu.Unlock()
	require.Empty(t, tree.objects)
}
