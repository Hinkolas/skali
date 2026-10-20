package build

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/require"
)

// observingSink records a push the way the deploy flow follows it: lines
// for the journal, snapshots for the live display.
type observingSink struct {
	mu       sync.Mutex
	lines    []string
	statuses []UploadStatus
}

func (s *observingSink) Line(_, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, message)
}

func (s *observingSink) UploadProgress(status UploadStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, status)
}

func (s *observingSink) snapshots() []UploadStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]UploadStatus(nil), s.statuses...)
}

func (s *observingSink) final(t *testing.T) UploadStatus {
	t.Helper()
	statuses := s.snapshots()
	require.NotEmpty(t, statuses)
	final := statuses[len(statuses)-1]
	require.True(t, final.Done, "the last snapshot is the final one")
	for _, status := range statuses[:len(statuses)-1] {
		require.False(t, status.Done)
	}
	return final
}

func (s *observingSink) lastLine() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lines[len(s.lines)-1]
}

func (s *observingSink) joined() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lines, "\n")
}

// testRegistry serves an in-memory registry on loopback, optionally
// wrapped to throttle or fail requests, and returns its host:port.
func testRegistry(t *testing.T, wrap func(http.Handler) http.Handler) string {
	t.Helper()
	var handler http.Handler = registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	if wrap != nil {
		handler = wrap(handler)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	return parsed.Host
}

// patchHandler routes blob data uploads (PATCH) to patch and everything
// else to the registry.
func patchHandler(patch func(next http.Handler, w http.ResponseWriter, r *http.Request)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPatch {
				patch(next, w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// layoutOf writes an image or index as the single artifact of an OCI
// layout, the shape a BuildKit export has.
func layoutOf(t *testing.T, artifact any) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "layout")
	path, err := layout.Write(dir, empty.Index)
	require.NoError(t, err)
	switch artifact := artifact.(type) {
	case v1.Image:
		require.NoError(t, path.AppendImage(artifact))
	case v1.ImageIndex:
		require.NoError(t, path.AppendIndex(artifact))
	}
	return dir
}

func digestOf(t *testing.T, artifact interface{ Digest() (v1.Hash, error) }) string {
	t.Helper()
	digest, err := artifact.Digest()
	require.NoError(t, err)
	return digest.String()
}

// A first push sends every blob; the second finds them all in the registry
// and reports that nothing was sent instead of claiming a transfer.
func TestPushLayoutReportsSentAndReusedBlobs(t *testing.T) {
	t.Parallel()
	host := testRegistry(t, nil)
	image, err := random.Image(64<<10, 3)
	require.NoError(t, err)
	dir := layoutOf(t, image)
	inventory, err := inventoryImage(image)
	require.NoError(t, err)

	first := &observingSink{}
	digest, err := pushLayout(context.Background(), dir, host+"/skali/demo/web:one", nil, first)
	require.NoError(t, err)
	require.Equal(t, digestOf(t, image), digest)
	status := first.final(t)
	require.Equal(t, 4, status.Blobs, "three layers and the config")
	require.Equal(t, inventory.total(), status.Total)
	require.Equal(t, status.Total, status.Sent)
	require.Equal(t, 4, status.SentBlobs)
	require.Zero(t, status.ReusedBlobs)
	require.Zero(t, status.Failures)
	require.Equal(t, UploadFinalizing, status.Phase)
	fraction, ok := status.Fraction()
	require.True(t, ok)
	require.Equal(t, 1.0, fraction)
	require.Contains(t, first.joined(), "pushing "+host+"/skali/demo/web:one ("+digest+"): 4 blobs, ")
	require.True(t, strings.HasPrefix(first.lastLine(), "pushed "+host+"/skali/demo/web:one ("+digest+") in "),
		first.lastLine())
	require.Contains(t, first.lastLine(), "in 4 blobs")

	second := &observingSink{}
	_, err = pushLayout(context.Background(), dir, host+"/skali/demo/web:two", nil, second)
	require.NoError(t, err)
	status = second.final(t)
	require.Zero(t, status.Sent, "reused blobs are not transferred")
	require.Zero(t, status.SentBlobs)
	require.Equal(t, 4, status.ReusedBlobs)
	require.Equal(t, status.Total, status.Reused)
	fraction, ok = status.Fraction()
	require.True(t, ok)
	require.Equal(t, 1.0, fraction, "nothing left to send")
	require.Contains(t, second.lastLine(), "nothing to send; 4 blobs")
	require.Contains(t, second.lastLine(), "already in the registry")

	// Pushing to the same tag again finds the manifest itself in place; the
	// pusher then checks no blob at all, and still none count as sent.
	third := &observingSink{}
	_, err = pushLayout(context.Background(), dir, host+"/skali/demo/web:two", nil, third)
	require.NoError(t, err)
	status = third.final(t)
	require.Zero(t, status.Sent)
	require.Equal(t, 4, status.ReusedBlobs)
	require.Equal(t, status.Total, status.Reused)
}

// A multi-platform index is inventoried across its images, and the push
// counts every platform's blobs.
func TestPushLayoutIndexCountsEveryPlatform(t *testing.T) {
	t.Parallel()
	host := testRegistry(t, nil)
	index, err := random.Index(32<<10, 2, 3)
	require.NoError(t, err)

	sink := &observingSink{}
	digest, err := pushLayout(context.Background(), layoutOf(t, index), host+"/skali/demo/multi:tag", nil, sink)
	require.NoError(t, err)
	require.Equal(t, digestOf(t, index), digest)
	status := sink.final(t)
	require.Equal(t, 9, status.Blobs, "three images of two layers and a config each")
	require.Equal(t, status.Total, status.Sent)
	require.Equal(t, 9, status.SentBlobs)
}

// The docker-save fallback reports the same way; layers shared with an
// image already in the registry count as reused, only new ones as sent.
func TestPushDockerSaveReportsPartialReuse(t *testing.T) {
	t.Parallel()
	host := testRegistry(t, nil)
	base, err := random.Image(64<<10, 2)
	require.NoError(t, err)
	_, err = pushLayout(context.Background(), layoutOf(t, base), host+"/skali/demo/base:tag", nil, DiscardSink{})
	require.NoError(t, err)

	added, err := random.Layer(48<<10, types.DockerLayer)
	require.NoError(t, err)
	image, err := mutate.AppendLayers(base, added)
	require.NoError(t, err)
	tarPath := filepath.Join(t.TempDir(), "image.tar")
	tag, err := name.NewTag(host + "/skali/demo/web:saved")
	require.NoError(t, err)
	require.NoError(t, tarball.WriteToFile(tarPath, tag, image))

	sink := &observingSink{}
	digest, err := pushDockerSave(context.Background(), tarPath, tag.String(), nil, sink)
	require.NoError(t, err)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, digest)
	status := sink.final(t)
	require.Equal(t, 4, status.Blobs)
	require.Equal(t, 2, status.ReusedBlobs, "the base layers were in the registry")
	require.Equal(t, 2, status.SentBlobs, "the added layer and the new config")
	needed, ok := status.Needed()
	require.True(t, ok)
	require.Equal(t, needed, status.Sent)
	require.Equal(t, status.Total, status.Sent+status.Reused)
	require.Contains(t, sink.joined(), "compressing the saved image layers")
}

// A throttled upload advances visibly while the bytes move, independent of
// anything else.
func TestUploadAdvancesWhileThrottled(t *testing.T) {
	t.Parallel()
	host := testRegistry(t, patchHandler(func(next http.Handler, w http.ResponseWriter, r *http.Request) {
		r.Body = &throttledBody{inner: r.Body, chunk: 32 << 10, pause: 15 * time.Millisecond}
		next.ServeHTTP(w, r)
	}))
	image, err := random.Image(6<<20, 1)
	require.NoError(t, err)

	sink := &observingSink{}
	_, err = pushLayout(context.Background(), layoutOf(t, image), host+"/skali/demo/slow:tag", nil, sink)
	require.NoError(t, err)

	needed, ok := sink.final(t).Needed()
	require.True(t, ok)
	var midway []int64
	var rated bool
	for _, status := range sink.snapshots() {
		if status.Phase == UploadSending && status.Sent > 0 && status.Sent < needed {
			if len(midway) == 0 || midway[len(midway)-1] != status.Sent {
				midway = append(midway, status.Sent)
			}
			rated = rated || status.Rate > 0
		}
	}
	require.GreaterOrEqual(t, len(midway), 3, "progress must advance during the transfer: %v", midway)
	for index := 1; index < len(midway); index++ {
		require.Greater(t, midway[index], midway[index-1], "without retries progress only grows")
	}
	require.True(t, rated, "the transfer rate becomes measurable")
}

// A connection that stops taking bytes shows as idle, with the sent count
// frozen; the push neither invents progress nor gives up on its own.
func TestUploadStallShowsIdleWithoutProgress(t *testing.T) {
	t.Parallel()
	host := testRegistry(t, patchHandler(func(next http.Handler, w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		next.ServeHTTP(w, r)
	}))
	image, err := random.Image(16<<20, 1)
	require.NoError(t, err)

	sink := &observingSink{}
	_, err = pushLayout(context.Background(), layoutOf(t, image), host+"/skali/demo/stall:tag", nil, sink)
	require.NoError(t, err)

	var stalled *UploadStatus
	statuses := sink.snapshots()
	for index := range statuses {
		if statuses[index].Transferring && statuses[index].Idle >= 700*time.Millisecond {
			stalled = &statuses[index]
			break
		}
	}
	require.NotNil(t, stalled, "the stall must show as idle")
	require.Equal(t, UploadSending, stalled.Phase)
	require.False(t, stalled.Retrying, "a stall is not a failure")
	require.Contains(t, IdleNote(*stalled), "no data sent for ")
	final := sink.final(t)
	require.Equal(t, final.Total, final.Sent)
}

// A failed attempt is surfaced and retried, and its bytes do not count
// twice.
func TestUploadRetryDoesNotDoubleCount(t *testing.T) {
	t.Parallel()
	var failed atomic.Bool
	host := testRegistry(t, patchHandler(func(next http.Handler, w http.ResponseWriter, r *http.Request) {
		if failed.CompareAndSwap(false, true) {
			_, _ = io.CopyN(io.Discard, r.Body, 32<<10)
			http.Error(w, "upstream hiccup", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	}))
	image, err := random.Image(256<<10, 1)
	require.NoError(t, err)

	sink := &observingSink{}
	_, err = pushLayout(context.Background(), layoutOf(t, image), host+"/skali/demo/retry:tag", nil, sink)
	require.NoError(t, err)
	final := sink.final(t)
	require.GreaterOrEqual(t, final.Failures, 1)
	require.False(t, final.Retrying, "a later success clears the retry state")
	require.Equal(t, final.Total, final.Sent, "the failed attempt's bytes are not counted")
	require.Contains(t, sink.joined(), "registry request failed, retrying: PATCH ")
	require.Contains(t, sink.joined(), "500 Internal Server Error")
}

// Cancelling mid-transfer ends the push with a cancellation, not a
// failure or a success.
func TestUploadCancellationIsReported(t *testing.T) {
	t.Parallel()
	// The server notices a client hanging up only once it reads the body,
	// so the held upload is released when the test ends.
	release := make(chan struct{})
	host := testRegistry(t, patchHandler(func(next http.Handler, w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release) })
	image, err := random.Image(1<<20, 1)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &observingSink{}
	go func() {
		for {
			for _, status := range sink.snapshots() {
				if status.Transferring {
					cancel()
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	_, err = pushLayout(ctx, layoutOf(t, image), host+"/skali/demo/cancel:tag", nil, sink)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, strings.HasPrefix(sink.lastLine(), "upload cancelled after "), sink.lastLine())
	require.Zero(t, sink.final(t).Failures, "a cancellation is not a registry failure")
}

// Imports report the managed-registry side of the copy the same way.
func TestImportReportsUpload(t *testing.T) {
	t.Parallel()
	upstream := testRegistry(t, nil)
	managed := testRegistry(t, nil)
	index, err := random.Index(32<<10, 1, 2)
	require.NoError(t, err)
	ref, err := name.ParseReference(upstream + "/library/app:1.0")
	require.NoError(t, err)
	require.NoError(t, remote.WriteIndex(ref, index))

	sink := &observingSink{}
	result, err := Import(context.Background(), ref.String(), managed+"/cache/app:imported", ImportOptions{}, sink)
	require.NoError(t, err)
	require.Equal(t, digestOf(t, index), result.Digest)
	status := sink.final(t)
	require.Equal(t, 4, status.Blobs)
	require.Equal(t, status.Total, status.Sent)
	require.Contains(t, sink.lastLine(), "pushed "+managed+"/cache/app:imported")

	again := &observingSink{}
	_, err = Import(context.Background(), ref.String(), managed+"/cache/app:again", ImportOptions{}, again)
	require.NoError(t, err)
	require.Zero(t, again.final(t).Sent)
	require.Equal(t, 4, again.final(t).ReusedBlobs)
}

// A failing push reports a failure line, and its error passes through.
func TestUploadFailureIsReported(t *testing.T) {
	t.Parallel()
	host := testRegistry(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") {
				http.Error(w, `{"errors":[{"code":"DENIED","message":"quota exceeded"}]}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	image, err := random.Image(16<<10, 1)
	require.NoError(t, err)

	sink := &observingSink{}
	_, err = pushLayout(context.Background(), layoutOf(t, image), host+"/skali/demo/denied:tag", nil, sink)
	require.Error(t, err)
	require.True(t, strings.HasPrefix(sink.lastLine(), "upload failed after "), sink.lastLine())
	final := sink.final(t)
	require.Equal(t, UploadFinalizing, final.Phase, "every blob landed; the manifest write failed")
	require.Equal(t, 1, final.Failures)
}

// fakeRegistry answers each request through respond, without a network.
type fakeRegistry func(*http.Request) *http.Response

func (f fakeRegistry) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	res := f(req)
	res.Request = req
	return res, nil
}

func response(status int, headers ...string) *http.Response {
	res := &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(""))}
	for index := 0; index+1 < len(headers); index += 2 {
		res.Header.Set(headers[index], headers[index+1])
	}
	return res
}

// Cross-repository mounts count as reused, and a commit refused after the
// data went through takes its bytes back.
func TestTrackerMountsAndRefusedCommits(t *testing.T) {
	t.Parallel()
	mounted := "sha256:" + strings.Repeat("a", 64)
	uploaded := "sha256:" + strings.Repeat("b", 64)
	tracker := newUploadTracker(time.Now())
	tracker.plan("", &blobInventory{sizes: map[string]int64{mounted: 1000, uploaded: 500}})
	commits := 0
	client := &http.Client{Transport: &trackingTransport{tracker: tracker, inner: fakeRegistry(func(req *http.Request) *http.Response {
		switch {
		case req.Method == http.MethodPost && req.URL.Query().Get("mount") == mounted:
			return response(http.StatusCreated)
		case req.Method == http.MethodPatch:
			return response(http.StatusAccepted, "Location", "/v2/app/blobs/uploads/session?state=2")
		case req.Method == http.MethodPut:
			commits++
			if commits == 1 {
				return response(http.StatusBadRequest)
			}
			return response(http.StatusCreated)
		}
		return response(http.StatusNotFound)
	})}}
	do := func(method, target string, body string) {
		req, err := http.NewRequest(method, "http://registry.test"+target, strings.NewReader(body))
		require.NoError(t, err)
		res, err := client.Do(req)
		require.NoError(t, err)
		res.Body.Close()
	}

	do(http.MethodPost, "/v2/app/blobs/uploads/?mount="+mounted+"&from=other", "")
	status := tracker.snapshot(time.Now())
	require.Equal(t, 1, status.ReusedBlobs)
	require.Equal(t, int64(1000), status.Reused)

	payload := strings.Repeat("x", 500)
	do(http.MethodPatch, "/v2/app/blobs/uploads/session?state=1", payload)
	require.Equal(t, int64(500), tracker.snapshot(time.Now()).Sent, "accepted data counts before the commit")
	do(http.MethodPut, "/v2/app/blobs/uploads/session?state=2&digest="+uploaded, "")
	status = tracker.snapshot(time.Now())
	require.Zero(t, status.Sent, "a refused commit takes its bytes back")
	require.Equal(t, 1, status.Failures)
	require.True(t, status.Retrying)

	do(http.MethodPatch, "/v2/app/blobs/uploads/session?state=1", payload)
	do(http.MethodPut, "/v2/app/blobs/uploads/session?state=2&digest="+uploaded, "")
	status = tracker.snapshot(time.Now())
	require.Equal(t, int64(500), status.Sent)
	require.Equal(t, 1, status.SentBlobs)
	require.False(t, status.Retrying)
	require.Equal(t, UploadFinalizing, status.Phase, "every blob is present")
}

func TestClassifyRegistryRequests(t *testing.T) {
	t.Parallel()
	digest := "sha256:" + strings.Repeat("c", 64)
	for _, test := range []struct {
		method, target string
		kind           requestKind
		digest         string
	}{
		{http.MethodHead, "/v2/a/b/blobs/" + digest, requestBlobCheck, digest},
		{http.MethodPost, "/v2/a/b/blobs/uploads/?mount=" + digest, requestBlobMount, digest},
		{http.MethodPost, "/v2/a/b/blobs/uploads/", requestBlobUpload, ""},
		{http.MethodPatch, "/v2/a/b/blobs/uploads/123", requestBlobData, ""},
		{http.MethodPut, "/v2/a/b/blobs/uploads/123?digest=" + digest, requestBlobCommit, digest},
		{http.MethodPut, "/v2/a/b/manifests/latest", requestManifest, "latest"},
		{http.MethodHead, "/v2/a/b/manifests/latest", requestManifestCheck, "latest"},
		{http.MethodGet, "/v2/", requestOther, ""},
	} {
		req, err := http.NewRequest(test.method, "https://registry.test"+test.target, nil)
		require.NoError(t, err)
		kind, digest := classify(req)
		require.Equal(t, test.kind, kind, "%s %s", test.method, test.target)
		require.Equal(t, test.digest, digest, "%s %s", test.method, test.target)
	}
}

func TestUploadStatusFractionAndETA(t *testing.T) {
	t.Parallel()
	unknown := UploadStatus{Phase: UploadSending, Sent: 100, Rate: 10}
	_, ok := unknown.Fraction()
	require.False(t, ok, "an unsized push has no fraction")
	_, ok = unknown.ETA()
	require.False(t, ok)

	status := UploadStatus{Phase: UploadSending, Blobs: 3, Total: 1000, Reused: 600, Sent: 100, Rate: 50}
	fraction, ok := status.Fraction()
	require.True(t, ok)
	require.Equal(t, 0.25, fraction, "the bar covers the needed bytes only")
	eta, ok := status.ETA()
	require.True(t, ok)
	require.Equal(t, 6*time.Second, eta)

	status.Idle = etaSteadyIdle
	_, ok = status.ETA()
	require.False(t, ok, "no ETA while bytes have stopped")
	status.Idle, status.Rate = 0, 0
	_, ok = status.ETA()
	require.False(t, ok, "no ETA before the rate is measurable")
}

// The journal narration stays sparse: one progress line per interval, the
// finalizing transition, and failures at most once per interval.
func TestUploadNarratorRateLimits(t *testing.T) {
	t.Parallel()
	narrator := &uploadNarrator{}
	at := func(elapsed time.Duration, status UploadStatus) []narration {
		status.Elapsed = elapsed
		return narrator.lines(status)
	}
	sending := UploadStatus{Phase: UploadSending, Blobs: 2, Total: 10 << 20, Sent: 5 << 20, Rate: 1 << 20}

	var lines []narration
	for elapsed := time.Duration(0); elapsed <= 40*time.Second; elapsed += uploadTick {
		lines = append(lines, at(elapsed, sending)...)
	}
	require.Len(t, lines, 2, "one line per narrateEvery: %v", lines)
	require.Equal(t, narration{"info", "sent 5.0MiB of 10.0MiB (50%) at 1.0MiB/s"}, lines[0])

	stalled := sending
	stalled.Transferring, stalled.Idle = true, 20*time.Second
	require.Equal(t, []narration{{"warn", "sent 5.0MiB of 10.0MiB (50%); no data sent for 20s"}},
		at(45*time.Second, stalled), "a stall is called out, without a stale rate")

	failing := sending
	failing.Failures, failing.Retrying, failing.LastError = 1, true, "PATCH /v2/x: 502 Bad Gateway"
	require.Equal(t, []narration{{"warn", "registry request failed, retrying: PATCH /v2/x: 502 Bad Gateway"}},
		at(46*time.Second, failing))
	failing.Failures = 3
	require.Empty(t, at(50*time.Second, failing), "failures are rate-limited")
	require.Equal(t, []narration{{"warn", "registry request failed (3 failures so far), retrying: PATCH /v2/x: 502 Bad Gateway"}},
		at(56*time.Second, failing))

	finalizing := UploadStatus{Phase: UploadFinalizing, Blobs: 2, Total: 10 << 20, Sent: 10 << 20, SentBlobs: 2}
	require.Equal(t, []narration{{"info", "all blobs are in the registry (sent 10.0MiB in 2 blobs); writing the manifest"}},
		at(57*time.Second, finalizing))
	require.Empty(t, at(58*time.Second, finalizing))
}

// throttledBody reads at most chunk bytes per pause, like a slow uplink.
type throttledBody struct {
	inner io.ReadCloser
	chunk int
	pause time.Duration
}

func (b *throttledBody) Read(p []byte) (int, error) {
	time.Sleep(b.pause)
	if len(p) > b.chunk {
		p = p[:b.chunk]
	}
	return b.inner.Read(p)
}

func (b *throttledBody) Close() error { return b.inner.Close() }

// An index push writes each platform's manifest as soon as its blobs are
// in; only the pushed artifact's own manifest starts the finalizing phase.
func TestTrackerFinalizesOnTheArtifactManifest(t *testing.T) {
	t.Parallel()
	child := "sha256:" + strings.Repeat("d", 64)
	top := "sha256:" + strings.Repeat("e", 64)
	tracker := newUploadTracker(time.Now())
	tracker.plan(top, &blobInventory{sizes: map[string]int64{"sha256:1": 10, "sha256:2": 10}})
	tracker.begin(requestBlobData, "", &bodyAttempt{})

	tracker.begin(requestManifest, child, nil)
	require.Equal(t, UploadChecking, tracker.snapshot(time.Now()).Phase, "a platform manifest is not the end")
	tracker.begin(requestManifest, "v1", nil)
	require.Equal(t, UploadFinalizing, tracker.snapshot(time.Now()).Phase)
}
