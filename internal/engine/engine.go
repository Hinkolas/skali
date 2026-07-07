// Package engine is the node-local container engine adapter — the bottom of
// the Executor seam. It wraps the Docker Engine API (Podman speaks the same
// protocol) behind the Engine interface and translates between skali's own
// spec/state types and the daemon's wire types. Higher layers (applications,
// database pools, system components) compile down to ContainerSpecs; nothing
// above this package speaks Docker.
//
// Two doctrines every implementation must honor:
//
//   - Ownership is label-driven. Everything skali creates carries
//     skali.managed=true (labels.go); everything else on the host is
//     invisible: List filters on the label, and Inspect and all mutating
//     operations refuse unlabeled containers. After a daemon or node restart
//     "what does skali run here" is rebuilt purely from labels — nodes keep
//     no local state. Inventory is the one deliberate exception: it reports
//     every image and volume on the node, managed or not, because its job is
//     whole-node disk visibility (images can't be label-stamped at pull, so
//     future image mutations enforce ownership via the registry mirror
//     prefix instead).
//   - No database, no eager connection. Constructing an engine touches
//     neither the daemon nor any other service: the master will eventually
//     boot engine-first (start engine → ensure its own control-plane
//     Postgres container → connect), so the engine must exist before
//     everything else does.
package engine

import (
	"context"
	"errors"
	"io"
	"time"
)

// Sentinel errors. Implementations wrap daemon errors into these; the gRPC
// and REST layers map them onto status codes.
var (
	ErrNotFound          = errors.New("engine: container not found")
	ErrConflict          = errors.New("engine: container name already in use")
	ErrNotManaged        = errors.New("engine: container is not managed by skali")
	ErrImageMissing      = errors.New("engine: image not present on node")
	ErrInvalidReference  = errors.New("engine: invalid image reference")
	ErrEngineUnavailable = errors.New("engine: container engine unavailable")
)

// PullPolicy says how Deploy resolves a spec's image.
type PullPolicy string

const (
	PullIfMissing PullPolicy = "if-missing" // pull only when absent (Deploy's default)
	PullAlways    PullPolicy = "always"
	PullNever     PullPolicy = "never" // fail with ErrImageMissing when absent
)

// RestartPolicy is the daemon-side restart behavior. With no reconciler on
// nodes yet, this is also what carries containers across node reboots.
type RestartPolicy string

const (
	RestartNone          RestartPolicy = "no"
	RestartAlways        RestartPolicy = "always"
	RestartUnlessStopped RestartPolicy = "unless-stopped"
	RestartOnFailure     RestartPolicy = "on-failure"
)

// Mount attaches host data to a container.
type Mount struct {
	Type     string // "bind" | "volume"
	Source   string // host path (bind) or volume name (volume)
	Target   string // path inside the container
	ReadOnly bool
}

// PortBinding publishes one container port on the host.
type PortBinding struct {
	HostIP        string // empty = all interfaces
	HostPort      uint16 // 0 = ephemeral
	ContainerPort uint16
	Protocol      string // "tcp" (default) | "udp"
}

// Healthcheck mirrors the daemon's HEALTHCHECK: it drives the container's
// health state, which later gates rollouts and Traefik routing.
type Healthcheck struct {
	Test        []string // e.g. {"CMD-SHELL", "curl -f http://localhost/ || exit 1"}
	Interval    time.Duration
	Timeout     time.Duration
	StartPeriod time.Duration
	Retries     int
}

// ContainerSpec is skali's own container description — the contract the
// application/database layers will compile down to. Labels must be complete
// (including skali.kind); Create force-stamps skali.managed regardless.
type ContainerSpec struct {
	Name              string
	Image             string
	Env               map[string]string
	Command           []string // overrides the image CMD
	Entrypoint        []string // overrides the image ENTRYPOINT
	Mounts            []Mount
	Ports             []PortBinding
	Restart           RestartPolicy // empty = "no"
	RestartMaxRetries int           // on-failure only
	NanoCPUs          int64         // 1e9 = one core; 0 = unlimited
	MemoryLimit       int64         // bytes; 0 = unlimited
	Networks          []string      // existing network names to attach
	Labels            map[string]string
	Healthcheck       *Healthcheck
}

// Container is one observed container.
type Container struct {
	ID           string
	Name         string // no leading slash
	Image        string
	State        string // created|running|paused|restarting|removing|exited|dead
	Health       string // "" when the container has no healthcheck
	ExitCode     int
	RestartCount int
	Labels       map[string]string
	CreatedAt    time.Time
	StartedAt    time.Time // zero when never started
}

// RawStats is one cumulative-counter reading. Rates and CPU percentages only
// exist as the delta between two readings — the Sampler derives them; nothing
// consumes RawStats directly.
type RawStats struct {
	At         time.Time
	CPUTotalNs uint64 // cumulative container CPU time
	OnlineCPUs uint32
	MemUsed    uint64 // working set: usage minus inactive page cache
	MemLimit   uint64 // as reported; the host total when unlimited
	NetRx      uint64 // cumulative bytes
	NetTx      uint64
}

// Stats is one derived per-container snapshot.
type Stats struct {
	CPUPercent float64 // docker-stats semantics: 100 = one full core
	MemUsed    uint64
	MemLimit   uint64
	NetRxRate  uint64 // bytes/second
	NetTxRate  uint64
}

// Image is one image present on the node, managed or not. An image with no
// repo tags is dangling ("<none>:<none>" placeholders are dropped at the
// adapter, they are danglingness, not tags).
type Image struct {
	ID          string // content-addressable "sha256:…"
	RepoTags    []string
	RepoDigests []string
	SizeBytes   int64
	Containers  int       // containers referencing it, any owner or state; 0 = unused
	CreatedAt   time.Time // zero when the daemon omits it
}

// Volume is one named volume on the node, managed or not (anonymous 64-hex
// volumes included). Sizes are deliberately absent: only the daemon's
// system-df walk computes them, far too expensive for a sampler.
type Volume struct {
	Name       string
	Driver     string
	Scope      string // "local" | "global"
	Mountpoint string
	Labels     map[string]string
	Containers int       // containers mounting it, any owner or state; 0 = unused
	CreatedAt  time.Time // zero when the daemon omits it
}

// Inventory is one consistent snapshot of everything on the node that
// occupies disk. In-use counts are joined against an unfiltered container
// listing at the adapter, so an image or volume referenced by anyone's
// container never looks unused.
type Inventory struct {
	Images  []Image
	Volumes []Volume
}

// Engine is the Executor seam's node-local surface. Implementations must
// wrap daemon failures into the sentinel errors above.
//
// List and Inspect see only skali-managed containers, and Start/Stop/Remove
// refuse unmanaged ones. Stats and Logs are read-only and trust their caller:
// ids are expected to come from List/Inspect, which already enforce the
// label boundary.
type Engine interface {
	Pull(ctx context.Context, image string) error
	ImageExists(ctx context.Context, image string) (bool, error)
	// InspectImage resolves a reference (tag or sha256: id) to the image's
	// observed state, including its in-use count.
	InspectImage(ctx context.Context, ref string) (Image, error)
	// RemoveImage removes (or merely untags) an image, returning the ids of
	// images actually deleted — empty when only a tag was removed from a
	// multi-tagged image. Images used by a container fail with ErrConflict
	// unless force.
	RemoveImage(ctx context.Context, ref string, force bool) ([]string, error)
	Create(ctx context.Context, spec ContainerSpec) (string, error)
	Start(ctx context.Context, id string) error
	// Stop gracefully stops a container; timeout <= 0 uses the daemon default.
	Stop(ctx context.Context, id string, timeout time.Duration) error
	Remove(ctx context.Context, id string, force bool) error
	Inspect(ctx context.Context, id string) (Container, error)
	List(ctx context.Context) ([]Container, error)
	// Inventory reports all images and volumes on the node — unfiltered,
	// read-only (see the package doc for why this bypasses the label
	// boundary).
	Inventory(ctx context.Context) (Inventory, error)
	Stats(ctx context.Context, id string) (RawStats, error)
	// Logs returns the daemon's log stream (multiplexed stdout/stderr framing
	// when the container has no TTY). tail <= 0 means the full log.
	Logs(ctx context.Context, id string, tail int, follow bool) (io.ReadCloser, error)
	// Events streams coarse change notifications until ctx is canceled. The
	// error channel yields exactly one error when the stream dies; callers
	// must resubscribe. Like Inventory, the stream is unfiltered — see the
	// package doc.
	Events(ctx context.Context) (<-chan EventKind, <-chan error)
}
