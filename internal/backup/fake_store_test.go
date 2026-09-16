package backup

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
)

// memoryStore is an in-memory objectStore for tests: a sorted key space
// with the same prefix semantics as S3, plus a log of every mutation so
// tests can assert ordering (manifest before components).
type memoryStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	// ops records Put/Remove/RemovePrefix calls in order, as "verb key".
	ops []string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{objects: make(map[string][]byte)}
}

func (m *memoryStore) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	m.ops = append(m.ops, "put "+key)
	return nil
}

func (m *memoryStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, errNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memoryStore) Stat(_ context.Context, key string) (objectStat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return objectStat{}, errNotFound
	}
	return objectStat{Size: int64(len(data))}, nil
}

func (m *memoryStore) keys(prefix string) []string {
	var keys []string
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (m *memoryStore) List(_ context.Context, prefix string, fn func(objectInfo) error) error {
	m.mu.Lock()
	keys := m.keys(prefix)
	sizes := make(map[string]int64, len(keys))
	for _, key := range keys {
		sizes[key] = int64(len(m.objects[key]))
	}
	m.mu.Unlock()
	for _, key := range keys {
		if err := fn(objectInfo{Key: key, Size: sizes[key]}); err != nil {
			return err
		}
	}
	return nil
}

func (m *memoryStore) ListPrefixes(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := make(map[string]bool)
	var prefixes []string
	for _, key := range m.keys(prefix) {
		rest := strings.TrimPrefix(key, prefix)
		head, _, found := strings.Cut(rest, "/")
		if !found {
			continue
		}
		child := prefix + head + "/"
		if !seen[child] {
			seen[child] = true
			prefixes = append(prefixes, child)
		}
	}
	return prefixes, nil
}

func (m *memoryStore) Remove(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ops = append(m.ops, "remove "+key)
	if _, ok := m.objects[key]; !ok {
		return errNotFound
	}
	delete(m.objects, key)
	return nil
}

func (m *memoryStore) RemovePrefix(_ context.Context, prefix string) (int64, error) {
	if err := checkRemovePrefix(prefix); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ops = append(m.ops, "remove-prefix "+prefix)
	var removed int64
	for _, key := range m.keys(prefix) {
		delete(m.objects, key)
		removed++
	}
	return removed, nil
}

func (m *memoryStore) Reachable(context.Context) error { return nil }

func (m *memoryStore) has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[key]
	return ok
}
