// Package enginetest provides an in-memory engine.Engine for tests: the
// sampler tests here and the cluster/API tests upstack drive it instead of a
// real daemon. Semantics mirror Docker where they matter (name conflicts,
// managed-label guard rails, remove-running-needs-force).
package enginetest

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Hinkolas/skali/internal/engine"
)

// Fake is a concurrency-safe in-memory Engine.
type Fake struct {
	mu     sync.Mutex
	nextID int
	byID   map[string]*engine.Container
	images map[string]bool

	eventSubs map[int]chan engine.EventKind
	eventNext int

	// StatsFn, when set, supplies raw counters per container id; unset, Stats
	// returns zeroed counters stamped with the current time.
	StatsFn func(id string) (engine.RawStats, error)
	// ListErr, when set, makes List fail — the "engine unreachable" seam.
	ListErr error
	// Pulled records every Pull, newest last.
	Pulled []string
	// Inv is what Inventory returns; InventoryErr is its unreachable seam.
	Inv          engine.Inventory
	InventoryErr error
	// EventsErr, when set, fails the NEXT Events subscription immediately and
	// then clears itself, so reconnect paths can be exercised.
	EventsErr error
}

func New(images ...string) *Fake {
	f := &Fake{
		byID:      map[string]*engine.Container{},
		images:    map[string]bool{},
		eventSubs: map[int]chan engine.EventKind{},
	}
	for _, img := range images {
		f.images[img] = true
	}
	return f
}

// Add seeds a container directly (bypassing image checks), returning its id.
func (f *Fake) Add(c engine.Container) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.ID == "" {
		f.nextID++
		c.ID = fmt.Sprintf("fake%060d", f.nextID)
	}
	f.byID[c.ID] = &c
	return c.ID
}

// Get returns a copy of a container regardless of labels (test inspection).
func (f *Fake) Get(id string) (engine.Container, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.byID[id]
	if !ok {
		return engine.Container{}, false
	}
	return *c, true
}

func (f *Fake) Pull(_ context.Context, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[image] = true
	f.Pulled = append(f.Pulled, image)
	// Mirror the pull into the inventory so InspectImage and heartbeat
	// reports see it, the way a real daemon would.
	if f.findImage(image) == nil {
		f.Inv.Images = append(f.Inv.Images, engine.Image{
			ID:       "sha256:fake-" + image,
			RepoTags: []string{image},
		})
	}
	return nil
}

func (f *Fake) ImageExists(_ context.Context, image string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images[image], nil
}

func (f *Fake) InspectImage(_ context.Context, ref string) (engine.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	img := f.findImage(ref)
	if img == nil {
		return engine.Image{}, fmt.Errorf("%w: %s", engine.ErrNotFound, ref)
	}
	return *img, nil
}

func (f *Fake) RemoveImage(_ context.Context, ref string, force bool) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	img := f.findImage(ref)
	if img == nil {
		return nil, fmt.Errorf("%w: %s", engine.ErrNotFound, ref)
	}
	if img.Containers > 0 && !force {
		return nil, fmt.Errorf("%w: image is in use", engine.ErrConflict)
	}
	removed := *img // copy: the compaction below invalidates the pointer
	kept := f.Inv.Images[:0]
	for _, i := range f.Inv.Images {
		if i.ID != removed.ID {
			kept = append(kept, i)
		}
	}
	f.Inv.Images = kept
	for _, t := range removed.RepoTags {
		delete(f.images, t)
	}
	return []string{removed.ID}, nil
}

// findImage resolves a reference (tag or id) against the inventory; callers
// hold the lock.
func (f *Fake) findImage(ref string) *engine.Image {
	for i := range f.Inv.Images {
		if f.Inv.Images[i].ID == ref || slices.Contains(f.Inv.Images[i].RepoTags, ref) {
			return &f.Inv.Images[i]
		}
	}
	return nil
}

func (f *Fake) Create(_ context.Context, spec engine.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if spec.Image == "" {
		return "", fmt.Errorf("engine: spec has no image")
	}
	if !f.images[spec.Image] {
		return "", fmt.Errorf("%w: %s", engine.ErrImageMissing, spec.Image)
	}
	for _, c := range f.byID {
		if c.Name == spec.Name && spec.Name != "" {
			return "", fmt.Errorf("%w: %s", engine.ErrConflict, spec.Name)
		}
	}
	labels := make(map[string]string, len(spec.Labels)+1)
	maps.Copy(labels, spec.Labels)
	labels[engine.LabelManaged] = "true"
	f.nextID++
	c := &engine.Container{
		ID:        fmt.Sprintf("fake%060d", f.nextID),
		Name:      spec.Name,
		Image:     spec.Image,
		State:     "created",
		Labels:    labels,
		CreatedAt: time.Now(),
	}
	if spec.Healthcheck != nil {
		c.Health = "starting"
	}
	f.byID[c.ID] = c
	return c.ID, nil
}

func (f *Fake) Start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.managed(id)
	if err != nil {
		return err
	}
	c.State = "running"
	c.StartedAt = time.Now()
	return nil
}

func (f *Fake) Stop(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.managed(id)
	if err != nil {
		return err
	}
	c.State = "exited"
	c.ExitCode = 0
	return nil
}

func (f *Fake) Remove(_ context.Context, id string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.managed(id)
	if err != nil {
		return err
	}
	if c.State == "running" && !force {
		return fmt.Errorf("%w: container is running", engine.ErrConflict)
	}
	delete(f.byID, id)
	return nil
}

func (f *Fake) Inspect(_ context.Context, id string) (engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.managed(id)
	if err != nil {
		return engine.Container{}, err
	}
	return *c, nil
}

func (f *Fake) List(_ context.Context) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	var out []engine.Container
	for _, c := range f.byID {
		if c.Labels[engine.LabelManaged] == "true" {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (f *Fake) Inventory(_ context.Context) (engine.Inventory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.InventoryErr != nil {
		return engine.Inventory{}, f.InventoryErr
	}
	return engine.Inventory{
		Images:  slices.Clone(f.Inv.Images),
		Volumes: slices.Clone(f.Inv.Volumes),
	}, nil
}

func (f *Fake) Stats(_ context.Context, id string) (engine.RawStats, error) {
	f.mu.Lock()
	statsFn := f.StatsFn
	_, ok := f.byID[id]
	f.mu.Unlock()
	if !ok {
		return engine.RawStats{}, fmt.Errorf("%w: %s", engine.ErrNotFound, id)
	}
	if statsFn != nil {
		return statsFn(id)
	}
	return engine.RawStats{At: time.Now()}, nil
}

func (f *Fake) Logs(_ context.Context, id string, _ int, _ bool) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byID[id]; !ok {
		return nil, fmt.Errorf("%w: %s", engine.ErrNotFound, id)
	}
	return io.NopCloser(strings.NewReader("")), nil
}

// Events mirrors the real adapter's contract: one error on the channel when
// the subscription dies (here: ctx cancellation or a seeded EventsErr), and
// events only while subscribed.
func (f *Fake) Events(ctx context.Context) (<-chan engine.EventKind, <-chan error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(chan engine.EventKind, 16)
	errs := make(chan error, 1)
	if f.EventsErr != nil {
		errs <- f.EventsErr
		f.EventsErr = nil
		return out, errs
	}
	f.eventNext++
	id := f.eventNext
	f.eventSubs[id] = out
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		delete(f.eventSubs, id)
		f.mu.Unlock()
		errs <- ctx.Err()
	}()
	return out, errs
}

// Subscribed reports the number of active Events subscriptions.
func (f *Fake) Subscribed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.eventSubs)
}

// Emit delivers an event to every active Events subscription (non-blocking:
// a full subscriber buffer drops the event, like a slow real consumer would).
func (f *Fake) Emit(kind engine.EventKind) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.eventSubs {
		select {
		case ch <- kind:
		default:
		}
	}
}

// managed is the fake's label boundary, mirroring the real adapter.
func (f *Fake) managed(id string) (*engine.Container, error) {
	c, ok := f.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", engine.ErrNotFound, id)
	}
	if c.Labels[engine.LabelManaged] != "true" {
		return nil, fmt.Errorf("%w: %s", engine.ErrNotManaged, id)
	}
	return c, nil
}
