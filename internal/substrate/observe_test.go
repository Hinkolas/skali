package substrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/observe"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
)

// storeDoer is a SeaweedFS that answers the probe's and the upkeep's
// requests from memory, counts them, and can hold every shell exec (the
// identity admin channel) until released, the way a slow `weed shell`
// holds the client's document lock.
type storeDoer struct {
	mu         sync.Mutex
	requests   int
	buckets    []string
	sizes      map[string]int64
	conf       seaweed.FilerConf
	confWrites int
	failWrites bool

	execStarted chan struct{}
	execGate    chan struct{}
}

func newStoreDoer() *storeDoer {
	return &storeDoer{sizes: map[string]int64{}, execStarted: make(chan struct{}, 16), execGate: make(chan struct{})}
}

func (d *storeDoer) ServiceProxyDo(_ context.Context, method, _, service string, _ int, path string, _ url.Values, body []byte) ([]byte, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests++
	answer := func(v any) ([]byte, int, error) {
		data, err := json.Marshal(v)
		return data, http.StatusOK, err
	}
	switch service {
	case seaweed.MasterService:
		switch path {
		case "/cluster/status":
			return answer(map[string]any{"Leader": "seaweed-master-0", "Peers": []string{}})
		case "/dir/status":
			return []byte(`{"Topology":{"DataCenters":[{"Racks":[{"DataNodes":[{"Url":"v0"}]}]}]}}`), http.StatusOK, nil
		case "/vol/status":
			var volumes []map[string]any
			id := 1
			for bucket, size := range d.sizes {
				volumes = append(volumes, map[string]any{"Id": id, "Size": size, "Collection": bucket})
				id++
			}
			return answer(map[string]any{"Volumes": map[string]any{"DataCenters": map[string]any{
				"dc1": map[string]any{"rack1": map[string]any{"v0:8080": volumes}},
			}}})
		}
	case seaweed.FilerService:
		switch {
		case path == "/":
			return []byte(`{}`), http.StatusOK, nil
		case path == seaweed.FilerConfPath && method == http.MethodGet:
			return answer(d.conf)
		case path == seaweed.FilerConfPath && method == http.MethodPut:
			if d.failWrites {
				return nil, http.StatusInternalServerError, nil
			}
			d.confWrites++
			var conf seaweed.FilerConf // whole-document replace, like the filer
			err := json.Unmarshal(body, &conf)
			d.conf = conf
			return nil, http.StatusOK, err
		case path == seaweed.BucketsPrefix:
			entries := make([]map[string]any, 0, len(d.buckets))
			for _, bucket := range d.buckets {
				entries = append(entries, map[string]any{"FullPath": seaweed.BucketsPrefix + bucket})
			}
			return answer(map[string]any{"Entries": entries})
		}
	case seaweed.S3Service:
		return nil, http.StatusForbidden, nil // anonymous refused
	}
	return nil, http.StatusNotFound, nil
}

func (d *storeDoer) ExecInPod(ctx context.Context, _, _, _ string, _ []string) (string, error) {
	d.execStarted <- struct{}{}
	select {
	case <-d.execGate:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "> {}\n", nil
}

func (d *storeDoer) ServiceAddress(context.Context, string, string, int) (string, error) {
	return "127.0.0.1:0", nil
}

func (d *storeDoer) ForgetServiceAddress(string, string, int) {}

func (d *storeDoer) requestCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requests
}

// storeFixture is the rotation fixture's provisioned bucket plus a
// controller speaking to a storeDoer.
type storeFixture struct {
	*rotationFixture
	store *storeDoer
}

func newStoreFixture(t *testing.T) *storeFixture {
	t.Helper()
	fx := newRotationFixture(t)
	doer := newStoreDoer()
	doer.buckets = []string{"b-files-01"}
	fx.control = New(Deps{
		DB:       fx.db,
		Cluster:  fx.fake,
		Observed: observe.NewStore(nil),
		Seaweed:  seaweed.NewClient(doer, Namespace),
	}, Config{Managed: false})
	return &storeFixture{rotationFixture: fx, store: doer}
}

// addBuckets provisions n more buckets in the fixture's environment.
func (fx *storeFixture) addBuckets(t *testing.T, n int) {
	t.Helper()
	ctx := context.Background()
	sw, err := fx.db.LiveObjectStore(ctx)
	require.NoError(t, err)
	for i := range n {
		key := fmt.Sprintf("extra%02d", i)
		owner := dbstore.ServiceOwner(*fx.claim.ProjectID, fx.envID, "demo", "production", key)
		row, err := fx.db.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
			Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled",
		})
		require.NoError(t, err)
		name := "b-" + key + "-01"
		_, err = fx.db.RecordAllocation(ctx, dbstore.AllocationInput{
			ClaimID: row.ID, StoreID: sw.ID, BucketName: name,
			AccessKeyID: "AK" + strings.ToUpper(key), CredentialSecret: "s3cred-" + key,
			Endpoint: InternalBucketEndpoint(), Region: seaweed.Region,
		})
		require.NoError(t, err)
		_, err = fx.db.TransitionBucketClaim(ctx, row.ID, claim.PhaseProvisioned)
		require.NoError(t, err)
		fx.store.mu.Lock()
		fx.store.buckets = append(fx.store.buckets, name)
		fx.store.mu.Unlock()
	}
}

func (fx *storeFixture) probe(t *testing.T, timeout time.Duration) ([]observe.Object, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return fx.control.SeaweedProbe()(ctx)
}

func bucketStatus(objects []observe.Object, name string) *module.BucketStatus {
	for _, object := range objects {
		if object.Kind == module.KindBucket && object.Ref.Name == name {
			return object.Bucket
		}
	}
	return nil
}

// The observation probe costs a fixed number of requests however many
// buckets exist: storage health on an installation with many buckets must
// not outgrow the probe's timeout the way per-bucket requests did.
func TestSeaweedProbeCostIsIndependentOfBucketCount(t *testing.T) {
	t.Parallel()
	fx := newStoreFixture(t)
	fx.store.conf = seaweed.FilerConf{Locations: []seaweed.PathConf{
		{LocationPrefix: seaweed.BucketsPrefix + "b-files-01/", ReadOnly: true},
	}}
	fx.store.sizes["b-files-01"] = 4096

	objects, err := fx.probe(t, 5*time.Second)
	require.NoError(t, err)
	one := fx.store.requestCount()
	files := bucketStatus(objects, "b-files-01")
	require.NotNil(t, files)
	require.True(t, files.Exists)
	require.True(t, files.ReadOnly, "the read-only state is read from filer.conf")
	require.EqualValues(t, 4096, files.UsedBytes)

	fx.addBuckets(t, 20)
	before := fx.store.requestCount()
	objects, err = fx.probe(t, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, objects, 1+21, "the store plus every bucket")
	require.Equal(t, one, fx.store.requestCount()-before, "twenty more buckets cost no more requests")
	extra := bucketStatus(objects, "b-extra07-01")
	require.NotNil(t, extra)
	require.True(t, extra.Exists)
	require.False(t, extra.ReadOnly)
}

// The probe takes no admin lock: an identity write blocked in a slow
// shell exec, holding the client's document lock, must not make storage
// health stale. This is the rc.1 failure, where every probe waited behind
// the claim workers' identity writes until its timeout.
func TestSeaweedProbeDoesNotWaitForAdminWrites(t *testing.T) {
	t.Parallel()
	fx := newStoreFixture(t)
	client := fx.control.deps.Seaweed

	done := make(chan error, 1)
	go func() {
		done <- client.EnsureIdentity(context.Background(), seaweed.Identity{Name: "b-files-01"})
	}()
	<-fx.store.execStarted // the write now holds the document lock

	objects, err := fx.probe(t, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, bucketStatus(objects, "b-files-01"))

	close(fx.store.execGate)
	require.NoError(t, <-done)
}

// Upkeep enforces every quota in one filer.conf write and only when a
// flag must move; a failing write is the upkeep's own problem and leaves
// observation untouched.
func TestStorageMaintenanceEnforcesQuotasInOneWrite(t *testing.T) {
	t.Parallel()
	fx := newStoreFixture(t)
	fx.addBuckets(t, 3)
	ctx := context.Background()

	fx.store.sizes["b-files-01"] = 2 << 30 // over its 1 GiB quota
	fx.store.sizes["b-extra01-01"] = 1 << 20
	fx.control.maintainStorage(ctx)
	require.Equal(t, 1, fx.store.confWrites)
	readOnly := map[string]bool{}
	for _, location := range fx.store.conf.Locations {
		readOnly[location.LocationPrefix] = location.ReadOnly
	}
	require.Equal(t, map[string]bool{seaweed.BucketsPrefix + "b-files-01/": true}, readOnly)

	fx.control.maintainStorage(ctx)
	require.Equal(t, 1, fx.store.confWrites, "a converged pass writes nothing")

	objects, err := fx.probe(t, 5*time.Second)
	require.NoError(t, err)
	require.True(t, bucketStatus(objects, "b-files-01").ReadOnly)

	// Back under quota: reopened, again in one write.
	fx.store.sizes["b-files-01"] = 1 << 20
	fx.control.maintainStorage(ctx)
	require.Equal(t, 2, fx.store.confWrites)
	require.False(t, fx.store.conf.Find(seaweed.BucketsPrefix+"b-files-01/").ReadOnly)

	// A filer that refuses the write fails the upkeep pass, not the probe.
	fx.store.failWrites = true
	fx.store.sizes["b-extra02-01"] = 2 << 30
	fx.control.maintainStorage(ctx)
	_, err = fx.probe(t, 5*time.Second)
	require.NoError(t, err)
}
