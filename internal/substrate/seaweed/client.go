package seaweed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Doer is the transport seam: one HTTP request against an in-cluster
// Service, and one exec in a ready pod. kube.Client implements both; the
// API server is the only path (identical from a dev laptop, where
// ClusterIPs are unreachable, and from inside the cluster). Non-2xx answers
// come back as data + status, not errors.
type Doer interface {
	ServiceProxyDo(ctx context.Context, method, namespace, service string, port int, path string, query url.Values, body []byte) ([]byte, int, error)
	// ExecInPod runs one command in a ready pod matching the selector, the
	// identity admin channel (`weed shell s3.configure`): SeaweedFS loads
	// identities through its credential manager and broadcasts updates to
	// the gateways over gRPC, so raw file writes are load-only and NOT a
	// supported update path (measured on the pin).
	ExecInPod(ctx context.Context, namespace, selector, container string, command []string) (string, error)
}

// Client is the admin surface of the substrate's external ensures:
// idempotent operations against the filer's file API and reads against the
// master. The identity and filer.conf documents are cluster-global
// single-writer state; the mutex serializes every read-modify-write so
// concurrent claim work can never interleave a lost update.
type Client struct {
	doer      Doer
	namespace string

	// filerSelector matches the pod carrying the filer admin channel: the
	// filer Deployment in production, the all-in-one pod on dev. Guarded by
	// its own mutex: shell() reads it while mu is held for a document RMW.
	targetMu       sync.Mutex
	filerSelector  string
	filerContainer string

	mu sync.Mutex // serializes identity / filer.conf RMW
}

// Every external call is bounded: a wedged transport (a proxy that cannot
// upgrade a stream, a half-dead conntrack entry) must never jam a substrate
// worker; the level-triggered requeue retries instead.
const (
	httpTimeout  = 30 * time.Second
	shellTimeout = 60 * time.Second
)

// NewClient wires the client to a transport and the platform namespace.
func NewClient(doer Doer, namespace string) *Client {
	return &Client{doer: doer, namespace: namespace}
}

// SetFilerTarget points the exec channel at the current filer pods; the
// substrate flips it between the production and dev shapes.
func (c *Client) SetFilerTarget(selector, container string) {
	c.targetMu.Lock()
	defer c.targetMu.Unlock()
	c.filerSelector = selector
	c.filerContainer = container
}

func (c *Client) filerTarget() (string, string) {
	c.targetMu.Lock()
	defer c.targetMu.Unlock()
	return c.filerSelector, c.filerContainer
}

func (c *Client) filer(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	return c.doer.ServiceProxyDo(ctx, method, c.namespace, FilerService, FilerPort, path, query, body)
}

func (c *Client) master(ctx context.Context, path string, query url.Values) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	return c.doer.ServiceProxyDo(ctx, http.MethodGet, c.namespace, MasterService, MasterPort, path, query, nil)
}

// EnsureBucket creates the bucket directory; one directory = one bucket =
// one collection. Idempotent: an existing directory answers 409, which is
// exactly the converged state. The trailing slash is semantic (mkdir).
func (c *Client) EnsureBucket(ctx context.Context, name string) error {
	_, status, err := c.filer(ctx, http.MethodPost, BucketsPrefix+name+"/", nil, nil)
	if err != nil {
		return err
	}
	if status == http.StatusConflict || (status >= 200 && status < 300) {
		return nil
	}
	return fmt.Errorf("seaweed: create bucket %s: status %d", name, status)
}

// BucketExists reports whether the bucket directory exists.
func (c *Client) BucketExists(ctx context.Context, name string) (bool, error) {
	_, status, err := c.filer(ctx, http.MethodGet, BucketsPrefix+name+"/", url.Values{"limit": {"1"}}, nil)
	if err != nil {
		return false, err
	}
	switch {
	case status == http.StatusNotFound:
		return false, nil
	case status >= 200 && status < 300:
		return true, nil
	}
	return false, fmt.Errorf("seaweed: stat bucket %s: status %d", name, status)
}

// DeleteBucket removes the bucket's metadata recursively; with the
// postgres2 store that drops the per-bucket table. skipChunkDeletion on
// purpose: the caller follows with DeleteCollection, which removes the
// volume files wholesale; letting the filer ALSO schedule per-chunk deletes
// races that and a volume server re-announces a half-deleted volume
// (measured on the pin: a needle-sized phantom collection survives).
// Missing is fine: deletion is level-triggered.
func (c *Client) DeleteBucket(ctx context.Context, name string) error {
	query := url.Values{"recursive": {"true"}, "ignoreRecursiveError": {"true"}, "skipChunkDeletion": {"true"}}
	_, status, err := c.filer(ctx, http.MethodDelete, BucketsPrefix+name+"/", query, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound || (status >= 200 && status < 300) {
		return nil
	}
	return fmt.Errorf("seaweed: delete bucket %s: status %d", name, status)
}

// DeleteCollection drops the bucket's collection on the master, the step
// that frees the volume files themselves the moment a bucket dies. The
// filer's recursive delete removes the metadata and the per-bucket table
// but only tombstones the chunk data; without this the bytes linger until
// vacuum. Any master proxies to the leader; a missing collection is
// converged (a bucket that never took a write has none).
func (c *Client) DeleteCollection(ctx context.Context, name string) error {
	data, status, err := c.master(ctx, "/col/delete", url.Values{"collection": {name}})
	if err != nil {
		return err
	}
	// "does not exist" answers 400 (measured on the pin): converged, like
	// 404.
	if (status >= 200 && status < 300) || status == http.StatusNotFound ||
		strings.Contains(string(data), "does not exist") {
		return nil
	}
	return fmt.Errorf("seaweed: delete collection %s: status %d: %s", name, status, data)
}

// shell runs s3.configure lines through `weed shell` in a filer pod, one
// exec per call. Inputs are generated identifiers (b-<key>-<id> names,
// alphanumeric keys); nothing user-controlled reaches the command line.
func (c *Client) shell(ctx context.Context, lines ...string) (string, error) {
	selector, container := c.filerTarget()
	if selector == "" {
		return "", fmt.Errorf("seaweed: no filer target configured")
	}
	master := MasterService + "." + c.namespace + ".svc.cluster.local:" + fmt.Sprint(MasterPort)
	script := fmt.Sprintf("echo '%s' | weed shell -master=%s -filer=localhost:%d",
		strings.Join(lines, "\n"), master, FilerPort)
	ctx, cancel := context.WithTimeout(ctx, shellTimeout)
	defer cancel()
	out, err := c.doer.ExecInPod(ctx, c.namespace, selector, container,
		[]string{"sh", "-c", script})
	if err != nil {
		return "", fmt.Errorf("seaweed: weed shell: %w", err)
	}
	return out, nil
}

// Identities reads the live identity set via `s3.configure` (print mode),
// the same channel writes go through, so reads always see what the
// gateways enforce. Absent config reads as empty, never an error.
func (c *Client) Identities(ctx context.Context) (IdentityConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.identities(ctx)
}

func (c *Client) identities(ctx context.Context) (IdentityConfig, error) {
	var cfg IdentityConfig
	out, err := c.shell(ctx, "s3.configure")
	if err != nil {
		return cfg, err
	}
	// The shell prints the config document among prompt noise; the JSON
	// object is the only braced block.
	start, end := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return cfg, nil // no config yet: a fresh store
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &cfg); err != nil {
		return cfg, fmt.Errorf("seaweed: parse identities: %w", err)
	}
	return cfg, nil
}

// EnsureIdentity converges one identity onto exactly ident: create it if
// absent, add/refresh the keypair, prune stray credentials (rotation is
// add-new + delete-old; seaweed appends keys, it never replaces). Settled
// identities are read-only passes. Serialized like every identity write.
func (c *Client) EnsureIdentity(ctx context.Context, ident Identity) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg, err := c.identities(ctx)
	if err != nil {
		return err
	}
	current := cfg.Find(ident.Name)

	var lines []string
	settled := current != nil && len(current.Credentials) == len(ident.Credentials) &&
		slices.Equal(current.Actions, ident.Actions)
	if settled {
		for i := range ident.Credentials {
			settled = settled && current.Credentials[i] == ident.Credentials[i]
		}
	}
	if settled {
		return nil
	}

	for _, cred := range ident.Credentials {
		lines = append(lines, fmt.Sprintf(
			"s3.configure -user=%s -access_key=%s -secret_key=%s -actions=%s -apply",
			ident.Name, cred.AccessKey, cred.SecretKey, strings.Join(ident.Actions, ",")))
	}
	if current != nil {
		keep := map[string]bool{}
		for _, cred := range ident.Credentials {
			keep[cred.AccessKey] = true
		}
		for _, cred := range current.Credentials {
			if !keep[cred.AccessKey] {
				lines = append(lines, fmt.Sprintf(
					"s3.configure -user=%s -access_key=%s -delete -apply", ident.Name, cred.AccessKey))
			}
		}
	}
	_, err = c.shell(ctx, lines...)
	return err
}

// DeleteIdentity removes an identity outright. Missing is converged;
// deletion is level-triggered like everything else.
func (c *Client) DeleteIdentity(ctx context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg, err := c.identities(ctx)
	if err != nil {
		return err
	}
	if cfg.Find(name) == nil {
		return nil
	}
	_, err = c.shell(ctx, fmt.Sprintf("s3.configure -user=%s -delete -apply", name))
	return err
}

// Conf reads the per-path configuration document; absent reads as empty.
func (c *Client) Conf(ctx context.Context) (FilerConf, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.confLocked(ctx)
}

// UpdateConf read-modify-writes filer.conf (the replication setting,
// per-bucket read-only flips). Filers hot-reload it; no restarts. The
// callback returns whether anything changed.
func (c *Client) UpdateConf(ctx context.Context, fn func(*FilerConf) bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg, err := c.confLocked(ctx)
	if err != nil {
		return err
	}
	if !fn(&cfg) {
		return nil
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, status, err := c.filer(ctx, http.MethodPut, FilerConfPath, nil, data)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("seaweed: write filer.conf: status %d", status)
	}
	return nil
}

func (c *Client) confLocked(ctx context.Context) (FilerConf, error) {
	var cfg FilerConf
	data, status, err := c.filer(ctx, http.MethodGet, FilerConfPath, nil, nil)
	if err != nil {
		return cfg, err
	}
	switch {
	case status == http.StatusNotFound:
		return cfg, nil
	case status < 200 || status >= 300:
		return cfg, fmt.Errorf("seaweed: read filer.conf: status %d", status)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("seaweed: parse filer.conf: %w", err)
	}
	return cfg, nil
}

// CollectionSizes reads every collection's footprint from the master's
// volume listing. Volumes are deduped by id: replicas appear once per node
// and quota charges logical bytes, not fleet disk. The empty-named default
// collection is skipped: buckets map 1:1 to collections named after them.
func (c *Client) CollectionSizes(ctx context.Context) (map[string]CollectionStat, error) {
	data, status, err := c.master(ctx, "/vol/status", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("seaweed: volume status: status %d", status)
	}
	var vs volStatus
	if err := json.Unmarshal(data, &vs); err != nil {
		return nil, fmt.Errorf("seaweed: parse volume status: %w", err)
	}

	seen := map[int64]bool{}
	stats := map[string]CollectionStat{}
	for _, dc := range vs.Volumes.DataCenters {
		for _, rack := range dc {
			for _, node := range rack {
				for _, vol := range node {
					if vol.Collection == "" || seen[vol.ID] {
						continue
					}
					seen[vol.ID] = true
					s := stats[vol.Collection]
					s.SizeBytes += vol.Size
					s.FileCount += vol.FileCount
					stats[vol.Collection] = s
				}
			}
		}
	}
	return stats, nil
}

// ClusterStatus reads the master cluster view: leadership and the peer
// list, the probe's raft-health input.
func (c *Client) ClusterStatus(ctx context.Context) (leader string, peers []string, err error) {
	data, status, err := c.master(ctx, "/cluster/status", nil)
	if err != nil {
		return "", nil, err
	}
	if status < 200 || status >= 300 {
		return "", nil, fmt.Errorf("seaweed: cluster status: status %d", status)
	}
	var cs clusterStatus
	if err := json.Unmarshal(data, &cs); err != nil {
		return "", nil, fmt.Errorf("seaweed: parse cluster status: %w", err)
	}
	return cs.Leader, cs.Peers, nil
}

// VolumeServerCount reads the number of connected volume servers from the
// master's directory topology; a server counts from the moment it
// announces, volumes or not.
func (c *Client) VolumeServerCount(ctx context.Context) (int, error) {
	data, status, err := c.master(ctx, "/dir/status", nil)
	if err != nil {
		return 0, err
	}
	if status < 200 || status >= 300 {
		return 0, fmt.Errorf("seaweed: dir status: status %d", status)
	}
	var ds dirStatus
	if err := json.Unmarshal(data, &ds); err != nil {
		return 0, fmt.Errorf("seaweed: parse dir status: %w", err)
	}
	count := 0
	for _, dc := range ds.Topology.DataCenters {
		for _, rack := range dc.Racks {
			count += len(rack.DataNodes)
		}
	}
	return count, nil
}

// FilerAlive reports whether the filer answers its HTTP API.
func (c *Client) FilerAlive(ctx context.Context) bool {
	_, status, err := c.filer(ctx, http.MethodGet, "/", url.Values{"limit": {"1"}}, nil)
	return err == nil && status >= 200 && status < 500
}

// S3Alive reports whether the S3 gateway answers; an anonymous request is
// denied (403), which is exactly an alive, authenticating gateway.
func (c *Client) S3Alive(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	_, status, err := c.doer.ServiceProxyDo(ctx, http.MethodGet, c.namespace, S3Service, S3Port, "/", nil, nil)
	return err == nil && status >= 200 && status < 500
}
