package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/Hinkolas/skali/internal/utils"
)

// UploadPhase names where a managed-registry push stands.
type UploadPhase string

const (
	// UploadPreparing reads (and for docker-save images compresses) the
	// image; the registry has not been contacted yet.
	UploadPreparing UploadPhase = "preparing"
	// UploadChecking talks to the registry (authentication, existence
	// checks) before any blob byte has been sent.
	UploadChecking UploadPhase = "checking"
	// UploadSending moves blob bytes.
	UploadSending UploadPhase = "uploading"
	// UploadFinalizing writes the manifests once every blob is present.
	UploadFinalizing UploadPhase = "finalizing"
)

// UploadStatus is one snapshot of a managed-registry push. Byte counts
// cover the distinct blobs (layers and configs) the push must make present
// in the registry; manifests are small and belong to the finalizing phase.
type UploadStatus struct {
	Phase UploadPhase
	// Blobs and Total describe the whole push. Both are zero when the image
	// could not be inventoried up front, which leaves the remaining work
	// unknown.
	Blobs int
	Total int64
	// ReusedBlobs and Reused count blobs the registry already had, found by
	// an existence check or mounted from another repository: never sent.
	ReusedBlobs int
	Reused      int64
	// SentBlobs counts uploads the registry committed. Sent counts the bytes
	// it accepted, including blobs still uploading; failed attempts that
	// were retried do not count.
	SentBlobs int
	Sent      int64
	// Rate is the network throughput in bytes per second over the recent
	// window, retransmissions included; zero until it can be measured.
	Rate    float64
	Elapsed time.Duration
	// Transferring holds while a blob body is being sent.
	Transferring bool
	// Idle is how long the push has gone without progress: without a byte
	// sent while transferring, without a registry response otherwise.
	Idle time.Duration
	// Failures counts failed registry requests. Retrying holds while the
	// latest failure has not been followed by a success; LastError
	// describes it.
	Failures  int
	Retrying  bool
	LastError string
	// Done marks the final snapshot, published once the push returned.
	Done bool
}

// Needed reports how many bytes the push sends in total, everything the
// registry did not already have, and whether that is known.
func (s UploadStatus) Needed() (int64, bool) {
	if s.Blobs == 0 {
		return 0, false
	}
	return max(s.Total-s.Reused, 0), true
}

// Fraction reports the sent share of the needed bytes, when known.
func (s UploadStatus) Fraction() (float64, bool) {
	needed, ok := s.Needed()
	if !ok {
		return 0, false
	}
	if needed == 0 {
		return 1, true
	}
	return min(float64(s.Sent)/float64(needed), 1), true
}

// etaSteadyIdle is how long bytes may stop before an ETA stops being
// believable.
const etaSteadyIdle = 3 * time.Second

// ETA estimates the remaining transfer time while bytes are flowing at a
// measurable rate; a stalled or unsized push has none.
func (s UploadStatus) ETA() (time.Duration, bool) {
	needed, ok := s.Needed()
	if !ok || s.Phase != UploadSending || s.Rate <= 0 || s.Idle >= etaSteadyIdle || s.Sent >= needed {
		return 0, false
	}
	return time.Duration(float64(needed-s.Sent) / s.Rate * float64(time.Second)).Round(time.Second), true
}

// UploadObserver is implemented by progress sinks that present a push
// structurally, for example as a progress bar. Snapshots arrive from a
// reporter goroutine every uploadTick while the push runs and once more,
// marked Done, after it returned; UploadProgress must return quickly but
// never holds up the transfer itself. Every sink also receives readable,
// rate-limited progress lines through Line.
type UploadObserver interface {
	UploadProgress(status UploadStatus)
}

// RegistryOf names the registry host of an image reference, for display.
func RegistryOf(reference string) string {
	ref, err := name.ParseReference(reference)
	if err != nil {
		return reference
	}
	return ref.Context().RegistryStr()
}

// blobInventory lists what a push must make present: the distinct
// distributable blobs with their sizes, and for every manifest (image or
// index) the blobs below it, since the pusher skips a manifest's whole
// subtree once the registry already has it. A nil inventory is unknown.
type blobInventory struct {
	sizes     map[string]int64
	manifests map[string][]string
}

// maxIndexDepth bounds nested indexes while inventorying.
const maxIndexDepth = 4

func newBlobInventory() *blobInventory {
	return &blobInventory{sizes: map[string]int64{}, manifests: map[string][]string{}}
}

func inventoryImage(image v1.Image) (*blobInventory, error) {
	inventory := newBlobInventory()
	if _, err := inventory.addImage(image); err != nil {
		return nil, err
	}
	return inventory, nil
}

func inventoryIndex(index v1.ImageIndex) (*blobInventory, error) {
	inventory := newBlobInventory()
	if _, err := inventory.addIndex(index, 0); err != nil {
		return nil, err
	}
	return inventory, nil
}

func (inventory *blobInventory) addImage(image v1.Image) ([]string, error) {
	digest, err := image.Digest()
	if err != nil {
		return nil, err
	}
	manifest, err := image.Manifest()
	if err != nil {
		return nil, err
	}
	blobs := []string{manifest.Config.Digest.String()}
	inventory.sizes[blobs[0]] = manifest.Config.Size
	for _, layer := range manifest.Layers {
		// The pusher skips foreign layers; they are never sent.
		if layer.MediaType.IsDistributable() {
			inventory.sizes[layer.Digest.String()] = layer.Size
			blobs = append(blobs, layer.Digest.String())
		}
	}
	inventory.manifests[digest.String()] = blobs
	return blobs, nil
}

func (inventory *blobInventory) addIndex(index v1.ImageIndex, depth int) ([]string, error) {
	if depth >= maxIndexDepth {
		return nil, fmt.Errorf("index nesting deeper than %d", maxIndexDepth)
	}
	digest, err := index.Digest()
	if err != nil {
		return nil, err
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		return nil, err
	}
	var blobs []string
	for _, child := range manifest.Manifests {
		var below []string
		switch {
		case child.MediaType.IsIndex():
			nested, err := index.ImageIndex(child.Digest)
			if err != nil {
				return nil, err
			}
			if below, err = inventory.addIndex(nested, depth+1); err != nil {
				return nil, err
			}
		case child.MediaType.IsImage():
			image, err := index.Image(child.Digest)
			if err != nil {
				return nil, err
			}
			if below, err = inventory.addImage(image); err != nil {
				return nil, err
			}
		}
		blobs = append(blobs, below...)
	}
	inventory.manifests[digest.String()] = blobs
	return blobs, nil
}

func (inventory *blobInventory) blobs() int {
	if inventory == nil {
		return 0
	}
	return len(inventory.sizes)
}

func (inventory *blobInventory) size(digest string) (int64, bool) {
	if inventory == nil {
		return 0, false
	}
	size, ok := inventory.sizes[digest]
	return size, ok
}

func (inventory *blobInventory) total() int64 {
	if inventory == nil {
		return 0
	}
	var total int64
	for _, size := range inventory.sizes {
		total += size
	}
	return total
}

// uploadTick is how often a running push publishes a snapshot.
const uploadTick = 200 * time.Millisecond

// upload follows one push: its tracker observes the registry traffic, and
// a reporter goroutine turns the tracker's state into snapshots for an
// observing sink and rate-limited lines for every sink. The transfer only
// ever takes the tracker's lock, so neither the display nor the journal
// can hold it up.
type upload struct {
	target   string
	sink     ProgressSink
	observer UploadObserver
	tracker  *uploadTracker

	digest   string
	stop     chan struct{}
	reported chan struct{}
}

// beginUpload starts following a push to target in the preparing phase.
// Every beginUpload must be paired with end.
func beginUpload(sink ProgressSink, target string) *upload {
	observer, _ := sink.(UploadObserver)
	u := &upload{
		target:   target,
		sink:     sink,
		observer: observer,
		tracker:  newUploadTracker(time.Now()),
		stop:     make(chan struct{}),
		reported: make(chan struct{}),
	}
	go u.report()
	return u
}

// plan records what is about to be pushed. An inventory error is not
// fatal: the push proceeds with an unknown total.
func (u *upload) plan(digest string, inventory *blobInventory, err error) {
	u.digest = digest
	message := "pushing " + u.target + " (" + digest + ")"
	if err != nil || inventory.blobs() == 0 {
		message += ", size unknown"
		inventory = nil
	} else {
		message += ": " + utils.PluralCount(inventory.blobs(), "blob") + ", " + utils.FormatBytes(inventory.total())
	}
	u.tracker.plan(digest, inventory)
	u.sink.Line("info", message)
}

// transport is the RoundTripper the push must use to be observed.
func (u *upload) transport() http.RoundTripper {
	return &trackingTransport{inner: remote.DefaultTransport, tracker: u.tracker}
}

func (u *upload) report() {
	defer close(u.reported)
	ticker := time.NewTicker(uploadTick)
	defer ticker.Stop()
	narrator := &uploadNarrator{}
	for {
		select {
		case <-u.stop:
			return
		case now := <-ticker.C:
			status := u.tracker.snapshot(now)
			if u.observer != nil {
				u.observer.UploadProgress(status)
			}
			for _, line := range narrator.lines(status) {
				u.sink.Line(line.level, line.message)
			}
		}
	}
}

// end stops following the push, publishes the final snapshot and the
// outcome line, and passes err through.
func (u *upload) end(ctx context.Context, err error) error {
	close(u.stop)
	<-u.reported
	status := u.tracker.snapshot(time.Now())
	status.Done = true
	if u.observer != nil {
		u.observer.UploadProgress(status)
	}
	level, message := u.outcome(ctx, status, err)
	u.sink.Line(level, message)
	return err
}

func (u *upload) outcome(ctx context.Context, status UploadStatus, err error) (level, message string) {
	elapsed := status.Elapsed.Round(time.Second)
	switch {
	case err == nil:
		message = "pushed " + u.target
		if u.digest != "" {
			message += " (" + u.digest + ")"
		}
		message += " in " + elapsed.String() + ": " + describeTransfer(status, u.tracker.averageRate())
		return "info", message
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		return "warn", "upload cancelled after " + elapsed.String() + " with " + describeSent(status)
	default:
		return "error", "upload failed after " + elapsed.String() + " with " + describeSent(status)
	}
}

// describeTransfer summarizes what a finished push moved and what it
// found already present.
func describeTransfer(status UploadStatus, average float64) string {
	var parts []string
	if status.SentBlobs == 0 && status.Sent == 0 {
		parts = append(parts, "nothing to send")
	} else {
		sent := "sent " + utils.FormatBytes(status.Sent)
		if status.SentBlobs > 0 {
			sent += " in " + utils.PluralCount(status.SentBlobs, "blob")
		}
		if average > 0 {
			sent += " at " + FormatRate(average)
		}
		parts = append(parts, sent)
	}
	if status.ReusedBlobs > 0 {
		parts = append(parts, utils.PluralCount(status.ReusedBlobs, "blob")+" ("+utils.FormatBytes(status.Reused)+
			") already in the registry")
	}
	return strings.Join(parts, "; ")
}

// describeSent states progress against the needed bytes when known.
func describeSent(status UploadStatus) string {
	if needed, ok := status.Needed(); ok {
		return utils.FormatBytes(status.Sent) + " of " + utils.FormatBytes(needed) + " sent"
	}
	return utils.FormatBytes(status.Sent) + " sent"
}

// FormatRate renders a throughput for humans.
func FormatRate(bytesPerSecond float64) string {
	return utils.FormatBytes(int64(bytesPerSecond)) + "/s"
}

// Journal narration intervals: a progress line at most every
// narrateEvery, a failure line at most every narrateFailuresEvery, and a
// stall called out once nothing moved for stallAfter.
const (
	narrateEvery         = 15 * time.Second
	narrateFailuresEvery = 10 * time.Second
	stallAfter           = 10 * time.Second
)

type narration struct{ level, message string }

// uploadNarrator turns snapshots into the rate-limited lines the journal
// and plain terminal output carry. It runs on the reporter goroutine only.
type uploadNarrator struct {
	lastProgress time.Duration // elapsed at the last progress line
	lastFailure  time.Duration // elapsed at the last failure line
	failures     int           // failures already narrated
	finalizing   bool
}

func (n *uploadNarrator) lines(status UploadStatus) []narration {
	var lines []narration
	if status.Failures > n.failures && (n.failures == 0 || status.Elapsed-n.lastFailure >= narrateFailuresEvery) {
		message := "registry request failed, retrying: " + status.LastError
		if status.Failures > 1 {
			message = fmt.Sprintf("registry request failed (%d failures so far), retrying: %s", status.Failures, status.LastError)
		}
		lines = append(lines, narration{"warn", message})
		n.failures = status.Failures
		n.lastFailure = status.Elapsed
	}
	if status.Phase == UploadFinalizing && !n.finalizing {
		n.finalizing = true
		n.lastProgress = status.Elapsed
		lines = append(lines, narration{"info", "all blobs are in the registry (" + describeTransfer(status, 0) +
			"); writing the manifest"})
		return lines
	}
	if status.Elapsed-n.lastProgress >= narrateEvery {
		n.lastProgress = status.Elapsed
		level := "info"
		if status.Idle >= stallAfter {
			level = "warn"
		}
		lines = append(lines, narration{level, describeUpload(status)})
	}
	return lines
}

// describeUpload renders a snapshot as one readable line: the plain-text
// counterpart of a progress bar.
func describeUpload(status UploadStatus) string {
	var parts []string
	switch status.Phase {
	case UploadPreparing:
		parts = append(parts, "preparing the image")
	case UploadChecking:
		parts = append(parts, "checking which blobs the registry already has")
	case UploadFinalizing:
		parts = append(parts, "writing the manifest")
	default:
		progress := "sent " + utils.FormatBytes(status.Sent)
		if needed, ok := status.Needed(); ok {
			fraction, _ := status.Fraction()
			progress += fmt.Sprintf(" of %s (%d%%)", utils.FormatBytes(needed), int(fraction*100))
		}
		if status.Rate > 0 && status.Idle < stallAfter {
			progress += " at " + FormatRate(status.Rate)
		}
		parts = append(parts, progress)
		if status.ReusedBlobs > 0 {
			parts = append(parts, utils.FormatBytes(status.Reused)+" already in the registry")
		}
	}
	if status.Idle >= stallAfter {
		parts = append(parts, IdleNote(status))
	}
	return strings.Join(parts, "; ")
}

// IdleNote says how long the push has gone without progress, in the terms
// of its phase.
func IdleNote(status UploadStatus) string {
	idle := status.Idle.Round(time.Second).String()
	switch {
	case status.Transferring:
		return "no data sent for " + idle
	case status.Phase == UploadPreparing:
		return "still preparing after " + idle
	default:
		return "waiting on the registry for " + idle
	}
}

// Rate measurement: throughput over the last rateWindow, reported once
// at least rateMinSpan of samples exist.
const (
	rateWindow  = 8 * time.Second
	rateMinSpan = time.Second
)

type rateSample struct {
	at   time.Time
	wire int64
}

// uploadTracker accumulates what the push's registry traffic shows. Every
// field is guarded by mu.
type uploadTracker struct {
	mu      sync.Mutex
	started time.Time

	inventory *blobInventory // nil while unknown
	top       string         // digest of the pushed artifact
	total     int64

	contacted     bool             // any registry request issued
	settled       map[string]bool  // blob digests known present (reused or committed)
	reusedBlobs   int              // settled by existence check or mount
	reused        int64            // their bytes
	sentBlobs     int              // settled by a committed upload
	committed     int64            // bytes of committed uploads
	staged        map[string]int64 // accepted bytes per upload session awaiting commit
	stagedBytes   int64            // their sum
	inflight      int64            // body bytes of requests still running
	sending       int              // requests whose body is being sent
	wire          int64            // every body byte read, retransmissions included
	firstByte     time.Time
	lastByte      time.Time
	lastSendStart time.Time // most recent body request start
	lastResponse  time.Time
	writingTop    bool // the pushed artifact's manifest write has started

	failures  int
	retrying  bool
	lastError string

	samples []rateSample
}

func newUploadTracker(now time.Time) *uploadTracker {
	return &uploadTracker{
		started: now,
		settled: map[string]bool{},
		staged:  map[string]int64{},
	}
}

func (t *uploadTracker) plan(digest string, inventory *blobInventory) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.top = digest
	t.inventory = inventory
	t.total = inventory.total()
}

// snapshot reports the push as of now and records a rate sample; only the
// reporter calls it while the push runs.
func (t *uploadTracker) snapshot(now time.Time) UploadStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	status := UploadStatus{
		Blobs:       t.inventory.blobs(),
		Total:       t.total,
		ReusedBlobs: t.reusedBlobs,
		Reused:      t.reused,
		SentBlobs:   t.sentBlobs,
		Sent:        t.committed + t.stagedBytes + t.inflight,
		Elapsed:     now.Sub(t.started),
		Failures:    t.failures,
		Retrying:    t.retrying,
		LastError:   t.lastError,
	}
	switch {
	case !t.contacted:
		status.Phase = UploadPreparing
	case t.writingTop || (t.inventory.blobs() > 0 && len(t.settled) >= t.inventory.blobs()):
		status.Phase = UploadFinalizing
	case t.wire > 0:
		status.Phase = UploadSending
	default:
		status.Phase = UploadChecking
	}
	status.Transferring = t.sending > 0
	since := t.started
	latest := func(times ...time.Time) {
		for _, at := range times {
			if at.After(since) {
				since = at
			}
		}
	}
	if status.Transferring {
		latest(t.lastByte, t.lastSendStart)
	} else {
		latest(t.lastResponse)
	}
	status.Idle = max(now.Sub(since), 0)

	t.samples = append(t.samples, rateSample{at: now, wire: t.wire})
	cut := 0
	for cut < len(t.samples)-1 && now.Sub(t.samples[cut].at) > rateWindow {
		cut++
	}
	t.samples = t.samples[cut:]
	if oldest := t.samples[0]; now.Sub(oldest.at) >= rateMinSpan {
		status.Rate = float64(t.wire-oldest.wire) / now.Sub(oldest.at).Seconds()
	}
	return status
}

// averageRate is the throughput between the first and last byte sent.
func (t *uploadTracker) averageRate() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	span := t.lastByte.Sub(t.firstByte)
	if t.wire == 0 || span < time.Second {
		return 0
	}
	return float64(t.wire) / span.Seconds()
}

// requestKind classifies registry requests by their part in a push.
type requestKind int

const (
	requestOther         requestKind = iota
	requestBlobCheck                 // HEAD /v2/<name>/blobs/<digest>
	requestBlobMount                 // POST /v2/<name>/blobs/uploads/?mount=<digest>
	requestBlobUpload                // POST /v2/<name>/blobs/uploads/
	requestBlobData                  // PATCH <upload location>
	requestBlobCommit                // PUT <upload location>?digest=<digest>
	requestManifest                  // PUT /v2/<name>/manifests/<reference>
	requestManifestCheck             // HEAD /v2/<name>/manifests/<reference>
)

func classify(req *http.Request) (kind requestKind, digest string) {
	path := req.URL.Path
	switch req.Method {
	case http.MethodHead:
		// Repository names may themselves contain "blobs" or "uploads", so
		// only the final segments decide.
		if index := strings.LastIndex(path, "/blobs/"); index >= 0 {
			if reference := path[index+len("/blobs/"):]; strings.Contains(reference, ":") && !strings.Contains(reference, "/") {
				return requestBlobCheck, reference
			}
		}
		if index := strings.LastIndex(path, "/manifests/"); index >= 0 {
			return requestManifestCheck, path[index+len("/manifests/"):]
		}
	case http.MethodPost:
		if strings.HasSuffix(strings.TrimSuffix(path, "/"), "/blobs/uploads") {
			if mount := req.URL.Query().Get("mount"); mount != "" {
				return requestBlobMount, mount
			}
			return requestBlobUpload, ""
		}
	case http.MethodPatch:
		return requestBlobData, ""
	case http.MethodPut:
		if index := strings.LastIndex(path, "/manifests/"); index >= 0 {
			return requestManifest, path[index+len("/manifests/"):]
		}
		if digest := req.URL.Query().Get("digest"); digest != "" {
			return requestBlobCommit, digest
		}
	}
	return requestOther, ""
}

// sessionKey identifies an upload session by its location without the
// query, which registries rewrite between chunks.
func sessionKey(location *url.URL) string {
	return location.Scheme + "://" + location.Host + location.Path
}

// trackingTransport observes a push's registry traffic beneath the
// pusher's retry and authentication layers, so it sees every attempt.
type trackingTransport struct {
	inner   http.RoundTripper
	tracker *uploadTracker
}

func (t *trackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	kind, digest := classify(req)
	var attempt *bodyAttempt
	if (kind == requestBlobData || kind == requestBlobCommit) && req.Body != nil && req.Body != http.NoBody {
		attempt = &bodyAttempt{}
		original := req
		req = req.Clone(req.Context())
		req.Body = &countingBody{tracker: t.tracker, attempt: attempt, inner: original.Body}
		if original.GetBody != nil {
			req.GetBody = func() (io.ReadCloser, error) {
				inner, err := original.GetBody()
				if err != nil {
					return nil, err
				}
				t.tracker.rewind(attempt)
				return &countingBody{tracker: t.tracker, attempt: attempt, inner: inner}, nil
			}
		}
	}
	t.tracker.begin(kind, digest, attempt)
	res, err := t.inner.RoundTrip(req)
	t.tracker.finish(req, kind, digest, attempt, res, err)
	return res, err
}

// bodyAttempt is the byte count of one request's body across the
// transport's rewinds. Its fields are guarded by the tracker's lock.
type bodyAttempt struct {
	count   int64 // bytes read since the last rewind
	settled bool  // finish has accounted for the request
}

// countingBody counts a request body as the transport reads it.
type countingBody struct {
	tracker *uploadTracker
	attempt *bodyAttempt
	inner   io.ReadCloser
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	if n > 0 {
		b.tracker.read(b.attempt, int64(n))
	}
	return n, err
}

func (b *countingBody) Close() error { return b.inner.Close() }

// rewind restarts the count of a body the transport is about to resend.
func (t *uploadTracker) rewind(attempt *bodyAttempt) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !attempt.settled {
		t.inflight -= attempt.count
	}
	attempt.count = 0
}

// read counts body bytes. Bytes the transport still reads after the
// response arrived count toward throughput only.
func (t *uploadTracker) read(attempt *bodyAttempt, n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if t.wire == 0 {
		t.firstByte = now
	}
	t.wire += n
	t.lastByte = now
	if !attempt.settled {
		attempt.count += n
		t.inflight += n
	}
}

func (t *uploadTracker) begin(kind requestKind, reference string, attempt *bodyAttempt) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.contacted = true
	if attempt != nil {
		t.sending++
		t.lastSendStart = time.Now()
	}
	// Index pushes write each platform's manifest (by digest) as soon as
	// its blobs are in, while other platforms may still upload; only the
	// pushed artifact's own manifest, written under the tag, comes last.
	if kind == requestManifest && (!strings.HasPrefix(reference, "sha256:") || reference == t.top) {
		t.writingTop = true
	}
}

// finish accounts for one completed attempt.
func (t *uploadTracker) finish(req *http.Request, kind requestKind, digest string, attempt *bodyAttempt,
	res *http.Response, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastResponse = time.Now()
	var accepted int64
	if attempt != nil {
		accepted = attempt.count
		t.inflight -= attempt.count
		attempt.settled = true
		t.sending--
	}
	ok := err == nil && res.StatusCode < 300
	switch kind {
	case requestBlobCheck:
		if err == nil && res.StatusCode == http.StatusOK {
			t.reuse(digest, res.ContentLength)
		}
	case requestBlobMount:
		if err == nil && res.StatusCode == http.StatusCreated {
			t.reuse(digest, -1)
		}
	case requestManifestCheck:
		// The pusher skips a manifest the registry already has, and with it
		// every blob below; the reference is a tag or the digest itself.
		// A tag check that finds another of the push's manifests (an image
		// now wrapped in an index) also makes the pusher skip that one.
		if err == nil && res.StatusCode == http.StatusOK && t.inventory != nil {
			present := res.Header.Get("Docker-Content-Digest")
			if present == "" {
				present = digest
			}
			for _, blob := range t.inventory.manifests[present] {
				t.reuse(blob, -1)
			}
		}
	case requestBlobData:
		if ok {
			key := sessionKey(req.URL)
			staged := t.staged[key] + accepted
			delete(t.staged, key)
			if location := res.Header.Get("Location"); location != "" {
				if next, err := req.URL.Parse(location); err == nil {
					key = sessionKey(next)
				}
			}
			t.staged[key] += staged
			t.stagedBytes += accepted
		}
	case requestBlobCommit:
		key := sessionKey(req.URL)
		staged := t.staged[key]
		switch {
		case err == nil && res.StatusCode == http.StatusUnauthorized:
			// The bearer transport refreshes its token and replays the
			// commit on the same session: the data stays accepted.
		case err == nil && res.StatusCode >= 300 && res.StatusCode < 400 && res.Header.Get("Location") != "":
			// The client follows the redirect with a new request.
			if next, err := req.URL.Parse(res.Header.Get("Location")); err == nil {
				delete(t.staged, key)
				t.staged[sessionKey(next)] += staged
			}
		default:
			delete(t.staged, key)
			t.stagedBytes -= staged
			if ok && !t.settled[digest] {
				t.settled[digest] = true
				t.sentBlobs++
				t.committed += staged + accepted
			}
		}
	}
	if t.failed(req, kind, res, err) {
		t.failures++
		t.retrying = true
		t.lastError = describeFailure(req, res, err)
	} else if ok && kind != requestOther {
		t.retrying = false
	}
}

func (t *uploadTracker) reuse(digest string, contentLength int64) {
	if t.settled[digest] {
		return
	}
	t.settled[digest] = true
	t.reusedBlobs++
	if size, ok := t.inventory.size(digest); ok {
		t.reused += size
	} else if contentLength > 0 {
		t.reused += contentLength
	}
}

// failed reports a push request worth surfacing: a transport error, a
// server or throttling status, or any refusal of an upload or manifest
// write. Authentication challenges, missing blobs or manifests, and the
// pusher's other traffic (the version check, which falls back from HTTPS
// to HTTP for loopback registries, and token requests) are the normal
// course of a push, and a cancelled push is not a registry failure.
func (t *uploadTracker) failed(req *http.Request, kind requestKind, res *http.Response, err error) bool {
	if kind == requestOther || req.Context().Err() != nil {
		return false
	}
	if err != nil {
		return true
	}
	status := res.StatusCode
	if status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout {
		return true
	}
	switch kind {
	case requestBlobUpload, requestBlobData, requestBlobCommit, requestManifest:
		return status >= 400 && status != http.StatusUnauthorized
	}
	return false
}

func describeFailure(req *http.Request, res *http.Response, err error) string {
	if err != nil {
		return err.Error()
	}
	return req.Method + " " + req.URL.Path + ": " + res.Status
}
