package kube

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/resourceversion"
)

// ObjectCache is a watch cache Apply can read an environment's objects from
// instead of the API server; the observation source implements it.
type ObjectCache interface {
	// CachedObject returns a copy of the cached object, or false when the
	// cache cannot vouch for it: the resource is not watched or not yet
	// synchronized, the watch is not fresh, or the object is absent.
	CachedObject(resource schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, bool)
}

// UseObjectCache lets Apply read environment objects from cache (see
// readForApply). Call it before the first Apply.
func (c *Client) UseObjectCache(cache ObjectCache) {
	c.reads.cache = cache
}

type cachedReadsKey struct{}

// WithCachedReads lets every Apply under ctx read an environment's objects
// from the cache (see readForApply); without it Apply reads live. The
// kernel's passes opt in, because a watch change to one of those objects
// enqueues its environment for the next pass, and leave out one pass per
// environment and audit interval: that pass checks what the cache vouched
// for and bounds how long a watch that silently stopped delivering goes
// unnoticed.
func WithCachedReads(ctx context.Context) context.Context {
	return context.WithValue(ctx, cachedReadsKey{}, true)
}

func cachedReads(ctx context.Context) bool {
	cached, _ := ctx.Value(cachedReadsKey{}).(bool)
	return cached
}

// seenFor is how long the client remembers a version the cache has not
// delivered yet. A cache that is still behind after it is stuck, and the
// kernel's live passes find it again (see WithCachedReads).
const seenFor = time.Hour

// readCache keeps the cache from answering with an object older than one
// this client already saw. Every version it reads from the API server or
// produces by a write is remembered until the cache delivers it or a newer
// one; an object it deleted or found absent is read live while the cache
// still holds it. Resource versions of one object are ordered (see
// resourceversion.CompareResourceVersion); one that cannot be compared
// reads live.
type readCache struct {
	cache ObjectCache // nil: every read is live

	mu    sync.Mutex
	seen  map[seenKey]seenVersion
	swept time.Time
}

type seenKey struct {
	resource        schema.GroupResource
	namespace, name string
}

type seenVersion struct {
	version string // empty: absent, or deleted by this client
	at      time.Time
}

// cached returns the cache's copy of the object when it is at least as new
// as every version this client saw of it.
func (r *readCache) cached(resource schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, bool) {
	if r.cache == nil {
		return nil, false
	}
	object, ok := r.cache.CachedObject(resource, namespace, name)
	if !ok {
		return nil, false
	}
	key := seenKey{resource: resource.GroupResource(), namespace: namespace, name: name}
	r.mu.Lock()
	defer r.mu.Unlock()
	if seen, ok := r.seen[key]; ok {
		if seen.version == "" {
			return nil, false
		}
		order, err := resourceversion.CompareResourceVersion(object.GetResourceVersion(), seen.version)
		if err != nil || order < 0 {
			return nil, false
		}
		// The cache caught up, and its watch only moves forward.
		delete(r.seen, key)
	}
	return object, true
}

// saw records the version of an object read from the API server or
// written; nil records it absent. An older version than one already
// recorded is ignored.
func (r *readCache) saw(resource schema.GroupVersionResource, namespace, name string, object *unstructured.Unstructured) {
	if r.cache == nil {
		return
	}
	version := ""
	if object != nil {
		version = object.GetResourceVersion()
	}
	key := seenKey{resource: resource.GroupResource(), namespace: namespace, name: name}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[seenKey]seenVersion{}
	}
	if now.Sub(r.swept) > seenFor {
		for key, seen := range r.seen {
			if now.Sub(seen.at) > seenFor {
				delete(r.seen, key)
			}
		}
		r.swept = now
	}
	if seen, ok := r.seen[key]; ok && version != "" && seen.version != "" {
		if order, err := resourceversion.CompareResourceVersion(version, seen.version); err == nil && order < 0 {
			return
		}
	}
	r.seen[key] = seenVersion{version: version, at: now}
}
