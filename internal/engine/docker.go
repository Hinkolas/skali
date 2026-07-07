package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

// Docker is the Engine implementation over the Docker Engine API. Podman's
// compatibility socket speaks the same protocol, so this is the only
// implementation planned; the interface exists so higher layers never depend
// on it directly.
type Docker struct {
	cli *client.Client
}

// NewDocker builds an engine for the daemon at socket (e.g.
// "unix:///var/run/docker.sock"). Construction is offline: the client dials
// lazily and negotiates the API version on first use, so a node whose engine
// is still booting (or absent) fails per-operation, never at startup.
func NewDocker(socket string) (*Docker, error) {
	cli, err := client.New(client.WithHost(socket))
	if err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	return &Docker{cli: cli}, nil
}

func (d *Docker) Close() error { return d.cli.Close() }

func (d *Docker) Pull(ctx context.Context, image string) error {
	// The client parses the reference before dialing and returns the parse
	// error untyped; validate here so garbage classifies instead of
	// surfacing as an opaque internal error.
	if _, err := reference.ParseNormalizedNamed(image); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidReference, err)
	}
	resp, err := d.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return classify(err)
	}
	defer resp.Close()
	// The pull only progresses while its progress stream is consumed.
	return classify(resp.Wait(ctx))
}

func (d *Docker) ImageExists(ctx context.Context, image string) (bool, error) {
	_, err := d.cli.ImageInspect(ctx, image)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, classify(err)
	}
	return true, nil
}

func (d *Docker) InspectImage(ctx context.Context, ref string) (Image, error) {
	res, err := d.cli.ImageInspect(ctx, ref)
	if err != nil {
		return Image{}, classify(err)
	}
	imageRefs, _, err := d.refCounts(ctx)
	if err != nil {
		return Image{}, err
	}
	img := Image{
		ID:          res.ID,
		RepoTags:    normalizeTags(res.RepoTags),
		RepoDigests: normalizeDigests(res.RepoDigests),
		SizeBytes:   res.Size,
		Containers:  imageRefs[res.ID],
	}
	img.CreatedAt, _ = time.Parse(time.RFC3339Nano, res.Created)
	return img, nil
}

func (d *Docker) RemoveImage(ctx context.Context, ref string, force bool) ([]string, error) {
	res, err := d.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{Force: force})
	if err != nil {
		return nil, classify(err)
	}
	var deleted []string
	for _, it := range res.Items {
		if it.Deleted != "" {
			deleted = append(deleted, it.Deleted)
		}
	}
	return deleted, nil
}

func (d *Docker) Create(ctx context.Context, spec ContainerSpec) (string, error) {
	cfg, host, netCfg, err := specConfigs(spec)
	if err != nil {
		return "", err
	}
	res, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:             spec.Name,
		Config:           cfg,
		HostConfig:       host,
		NetworkingConfig: netCfg,
	})
	if err != nil {
		// The daemon reports a missing image as a generic 404; keep the
		// distinction so PullNever failures stay actionable.
		if cerrdefs.IsNotFound(err) && strings.Contains(err.Error(), "No such image") {
			return "", fmt.Errorf("%w: %s", ErrImageMissing, spec.Image)
		}
		return "", classify(err)
	}
	return res.ID, nil
}

func (d *Docker) Start(ctx context.Context, id string) error {
	if _, err := d.inspectManaged(ctx, id); err != nil {
		return err
	}
	_, err := d.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return classify(err)
}

func (d *Docker) Stop(ctx context.Context, id string, timeout time.Duration) error {
	if _, err := d.inspectManaged(ctx, id); err != nil {
		return err
	}
	opts := client.ContainerStopOptions{}
	if timeout > 0 {
		secs := int(timeout.Round(time.Second) / time.Second)
		opts.Timeout = &secs
	}
	_, err := d.cli.ContainerStop(ctx, id, opts)
	return classify(err)
}

func (d *Docker) Remove(ctx context.Context, id string, force bool) error {
	if _, err := d.inspectManaged(ctx, id); err != nil {
		return err
	}
	_, err := d.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: force})
	return classify(err)
}

func (d *Docker) Inspect(ctx context.Context, id string) (Container, error) {
	in, err := d.inspectManaged(ctx, id)
	if err != nil {
		return Container{}, err
	}
	return containerFromInspect(in), nil
}

// List returns skali-managed containers in every state, at summary detail:
// exit codes, start times, and restart counts need Inspect.
func (d *Docker) List(ctx context.Context) ([]Container, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", LabelManaged+"=true"),
	})
	if err != nil {
		return nil, classify(err)
	}
	out := make([]Container, 0, len(res.Items))
	for _, s := range res.Items {
		out = append(out, containerFromSummary(s))
	}
	return out, nil
}

// Inventory lists every image and volume on the node in one pass. One
// unfiltered container listing (any owner, any state) feeds the in-use
// counts: an image or volume referenced by anyone's container must never
// look unused.
func (d *Docker) Inventory(ctx context.Context) (Inventory, error) {
	imgRes, err := d.cli.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return Inventory{}, classify(err)
	}
	volRes, err := d.cli.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return Inventory{}, classify(err)
	}
	imageRefs, volumeRefs, err := d.refCounts(ctx)
	if err != nil {
		return Inventory{}, err
	}

	inv := Inventory{
		Images:  make([]Image, 0, len(imgRes.Items)),
		Volumes: make([]Volume, 0, len(volRes.Items)),
	}
	for _, s := range imgRes.Items {
		inv.Images = append(inv.Images, imageFromSummary(s, imageRefs[s.ID]))
	}
	for _, v := range volRes.Items {
		inv.Volumes = append(inv.Volumes, volumeFromAPI(v, volumeRefs[v.Name]))
	}
	return inv, nil
}

func (d *Docker) Stats(ctx context.Context, id string) (RawStats, error) {
	res, err := d.cli.ContainerStats(ctx, id, client.ContainerStatsOptions{})
	if err != nil {
		return RawStats{}, classify(err)
	}
	defer res.Body.Close()
	var sr container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&sr); err != nil {
		return RawStats{}, fmt.Errorf("engine: decode stats: %w", err)
	}
	return rawFromStats(sr), nil
}

func (d *Docker) Logs(ctx context.Context, id string, tail int, follow bool) (io.ReadCloser, error) {
	opts := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: follow, Tail: "all"}
	if tail > 0 {
		opts.Tail = strconv.Itoa(tail)
	}
	res, err := d.cli.ContainerLogs(ctx, id, opts)
	if err != nil {
		return nil, classify(err)
	}
	return res, nil
}

// inspectManaged is the label boundary: every by-id read or mutation goes
// through it, so an id belonging to someone else's container is refused
// before the daemon is asked to act.
func (d *Docker) inspectManaged(ctx context.Context, id string) (container.InspectResponse, error) {
	res, err := d.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, classify(err)
	}
	c := res.Container
	if c.Config == nil || c.Config.Labels[LabelManaged] != "true" {
		return container.InspectResponse{}, fmt.Errorf("%w: %s", ErrNotManaged, id)
	}
	return c, nil
}

func containerFromInspect(in container.InspectResponse) Container {
	c := Container{
		ID:           in.ID,
		Name:         strings.TrimPrefix(in.Name, "/"),
		RestartCount: in.RestartCount,
	}
	if in.Config != nil {
		c.Image = in.Config.Image
		c.Labels = in.Config.Labels
	}
	if in.State != nil {
		c.State = string(in.State.Status)
		c.ExitCode = in.State.ExitCode
		if in.State.Health != nil {
			c.Health = string(in.State.Health.Status)
		}
		// Never-started containers carry the zero timestamp; the parse error
		// path lands on the same zero value.
		c.StartedAt, _ = time.Parse(time.RFC3339Nano, in.State.StartedAt)
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, in.Created)
	return c
}

func containerFromSummary(s container.Summary) Container {
	c := Container{
		ID:        s.ID,
		Image:     s.Image,
		State:     string(s.State),
		Labels:    s.Labels,
		CreatedAt: time.Unix(s.Created, 0),
	}
	if len(s.Names) > 0 {
		c.Name = strings.TrimPrefix(s.Names[0], "/")
	}
	if s.Health != nil && s.Health.Status != container.NoHealthcheck {
		c.Health = string(s.Health.Status)
	}
	return c
}

// refCounts joins one unfiltered container listing (any owner, any state)
// into per-image and per-volume in-use counts: an image or volume referenced
// by anyone's container must never look unused.
func (d *Docker) refCounts(ctx context.Context) (imageRefs, volumeRefs map[string]int, err error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, nil, classify(err)
	}
	imageRefs = make(map[string]int, len(res.Items))
	volumeRefs = make(map[string]int)
	for _, c := range res.Items {
		imageRefs[c.ImageID]++
		for _, m := range c.Mounts {
			if m.Type == mount.TypeVolume && m.Name != "" {
				volumeRefs[m.Name]++
			}
		}
	}
	return imageRefs, volumeRefs, nil
}

func imageFromSummary(s image.Summary, refs int) Image {
	img := Image{
		ID:          s.ID,
		RepoTags:    normalizeTags(s.RepoTags),
		RepoDigests: normalizeDigests(s.RepoDigests),
		SizeBytes:   s.Size,
		Containers:  refs,
	}
	if s.Created > 0 {
		img.CreatedAt = time.Unix(s.Created, 0)
	}
	return img
}

// "<none>" placeholders express danglingness; they are not tags.
func normalizeTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if t != "<none>:<none>" {
			out = append(out, t)
		}
	}
	return out
}

func normalizeDigests(digests []string) []string {
	var out []string
	for _, d := range digests {
		if d != "<none>@<none>" {
			out = append(out, d)
		}
	}
	return out
}

func volumeFromAPI(v volume.Volume, refs int) Volume {
	out := Volume{
		Name:       v.Name,
		Driver:     v.Driver,
		Scope:      v.Scope,
		Mountpoint: v.Mountpoint,
		Labels:     v.Labels,
		Containers: refs,
	}
	// Omitted or unparseable timestamps land on the zero value.
	out.CreatedAt, _ = time.Parse(time.RFC3339Nano, v.CreatedAt)
	return out
}

// rawFromStats reduces a daemon stats sample to skali's cumulative counters.
func rawFromStats(sr container.StatsResponse) RawStats {
	r := RawStats{
		At:         sr.Read,
		CPUTotalNs: sr.CPUStats.CPUUsage.TotalUsage,
		OnlineCPUs: sr.CPUStats.OnlineCPUs,
		MemLimit:   sr.MemoryStats.Limit,
	}
	if r.At.IsZero() {
		r.At = time.Now()
	}
	// Working set, kubelet-style: drop evictable page cache so "used" means
	// memory the container would fight to keep. cgroup v2 exposes
	// inactive_file, v1 total_inactive_file.
	used := sr.MemoryStats.Usage
	if v, ok := sr.MemoryStats.Stats["inactive_file"]; ok && v < used {
		used -= v
	} else if v, ok := sr.MemoryStats.Stats["total_inactive_file"]; ok && v < used {
		used -= v
	}
	r.MemUsed = used
	for _, n := range sr.Networks {
		r.NetRx += n.RxBytes
		r.NetTx += n.TxBytes
	}
	return r
}

// classify wraps daemon errors into the package sentinels so callers can
// switch on errors.Is without knowing the client library.
func classify(err error) error {
	switch {
	case err == nil:
		return nil
	case client.IsErrConnectionFailed(err):
		return fmt.Errorf("%w: %v", ErrEngineUnavailable, err)
	case cerrdefs.IsNotFound(err):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case cerrdefs.IsConflict(err):
		return fmt.Errorf("%w: %v", ErrConflict, err)
	case cerrdefs.IsInvalidArgument(err):
		return fmt.Errorf("%w: %v", ErrInvalidReference, err)
	default:
		return err
	}
}
