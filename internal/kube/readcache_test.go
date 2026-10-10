package kube

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	kt "k8s.io/client-go/testing"
)

var secretResource = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// objectCache is a watch cache the test moves by hand.
type objectCache struct {
	mu      sync.Mutex
	objects map[string]*unstructured.Unstructured
}

func (c *objectCache) CachedObject(_ schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	object, ok := c.objects[namespace+"/"+name]
	if !ok {
		return nil, false
	}
	return object.DeepCopy(), true
}

func (c *objectCache) set(object *unstructured.Unstructured) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects[object.GetNamespace()+"/"+object.GetName()] = object.DeepCopy()
}

// cachedClient serves want from both the cluster and the cache, and
// answers every apply with the next resource version.
func cachedClient(t *testing.T, want *unstructured.Unstructured) (*Client, *fake.FakeDynamicClient, *objectCache, *[]string) {
	t.Helper()
	client, dynamic := ownershipClient(want)
	cache := &objectCache{objects: map[string]*unstructured.Unstructured{}}
	cache.set(want)
	client.UseObjectCache(cache)
	var preconditions []string
	version := 7
	dynamic.PrependReactor("patch", "secrets", func(action kt.Action) (bool, runtime.Object, error) {
		applied := &unstructured.Unstructured{}
		require.NoError(t, json.Unmarshal(action.(kt.PatchAction).GetPatch(), applied))
		preconditions = append(preconditions, applied.GetResourceVersion())
		version++
		live := want.DeepCopy()
		live.SetResourceVersion(strconv.Itoa(version))
		require.NoError(t, dynamic.Tracker().Update(secretResource, live, live.GetNamespace()))
		return true, live, nil
	})
	return client, dynamic, cache, &preconditions
}

func reads(dynamic *fake.FakeDynamicClient) int {
	count := 0
	for _, action := range dynamic.Actions() {
		if action.GetVerb() == "get" {
			count++
		}
	}
	return count
}

// An environment's object is read from the cache, and its apply carries
// the cached version as its precondition.
func TestApplyReadsFromCache(t *testing.T) {
	want := ownedSecret(uuid.NewString())
	client, dynamic, _, preconditions := cachedClient(t, want)
	_, err := client.Apply(WithCachedReads(context.Background()), want, false)
	require.NoError(t, err)
	require.Zero(t, reads(dynamic))
	require.Equal(t, []string{"7"}, *preconditions)
}

// A cache that has not delivered this client's own write is not read
// until it does.
func TestApplyReadsLiveUntilCacheHasOwnWrite(t *testing.T) {
	want := ownedSecret(uuid.NewString())
	client, dynamic, cache, preconditions := cachedClient(t, want)
	ctx := WithCachedReads(context.Background())
	_, err := client.Apply(ctx, want, false)
	require.NoError(t, err)

	_, err = client.Apply(ctx, want, false)
	require.NoError(t, err)
	require.Equal(t, 1, reads(dynamic), "the cache still holds version 7")
	require.Equal(t, []string{"7", "8"}, *preconditions)

	live, err := dynamic.Resource(secretResource).Namespace(want.GetNamespace()).Get(ctx, want.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	cache.set(live)
	before := reads(dynamic)
	_, err = client.Apply(ctx, want, false)
	require.NoError(t, err)
	require.Equal(t, before, reads(dynamic), "the cache caught up")
	require.Equal(t, []string{"7", "8", "9"}, *preconditions)
}

// An object this client deleted is read live while the cache still holds
// it, and so is every apply that is forced, not an environment's, or not
// under WithCachedReads.
func TestApplyReadsLiveOnDoubt(t *testing.T) {
	ctx := WithCachedReads(context.Background())
	t.Run("deleted", func(t *testing.T) {
		want := ownedSecret(uuid.NewString())
		client, dynamic, _, _ := cachedClient(t, want)
		_, err := client.Delete(ctx, ObjectRef{GVK: secretKind, Namespace: want.GetNamespace(), Name: want.GetName()})
		require.NoError(t, err)
		_, err = client.Apply(ctx, want, false)
		require.NoError(t, err)
		require.True(t, created(dynamic), "the namesake is created, not patched from the cache")
	})
	for name, apply := range map[string]func(*Client, *objectCache, *unstructured.Unstructured) error{
		"forced": func(client *Client, _ *objectCache, want *unstructured.Unstructured) error {
			_, err := client.Apply(ctx, want, true)
			return err
		},
		"platform": func(client *Client, cache *objectCache, want *unstructured.Unstructured) error {
			platform := want.DeepCopy()
			platform.SetNamespace("skali-platform")
			platform.SetLabels(nil)
			cache.set(platform)
			_, err := client.Apply(ctx, platform, false)
			return err
		},
		"live reads": func(client *Client, _ *objectCache, want *unstructured.Unstructured) error {
			_, err := client.Apply(context.Background(), want, false)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			want := ownedSecret(uuid.NewString())
			client, dynamic, cache, _ := cachedClient(t, want)
			require.NoError(t, apply(client, cache, want))
			require.Equal(t, 1, reads(dynamic))
		})
	}
}

// A conflict after a cached read retries with a live read and accepts the
// object that read finds: the cache may predate a replacement.
func TestApplyRetriesLiveAfterCachedConflict(t *testing.T) {
	want := ownedSecret(uuid.NewString())
	client, dynamic, _, preconditions := cachedClient(t, want)
	replaced := want.DeepCopy()
	replaced.SetUID(types.UID("second"))
	replaced.SetResourceVersion("12")
	require.NoError(t, dynamic.Tracker().Update(secretResource, replaced, replaced.GetNamespace()))
	calls := 0
	dynamic.PrependReactor("patch", "secrets", func(kt.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, nil, apierrors.NewConflict(secretResource.GroupResource(), want.GetName(), errors.New("object modified"))
		}
		return false, nil, nil
	})
	_, err := client.Apply(WithCachedReads(context.Background()), want, false)
	require.NoError(t, err)
	require.Equal(t, 1, reads(dynamic))
	require.Equal(t, []string{"12"}, *preconditions, "the retry applies against the live object")
}

func created(dynamic *fake.FakeDynamicClient) bool {
	for _, action := range dynamic.Actions() {
		if action.GetVerb() == "create" {
			return true
		}
	}
	return false
}
