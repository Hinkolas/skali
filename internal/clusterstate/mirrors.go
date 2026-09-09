package clusterstate

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MirrorReconciler is used only by the leader. Mirrors are diagnostic projections,
// never transaction authority. Its cache is rebuilt from Kubernetes after restart
// and periodically refreshed to repair missing or externally changed resources.
type MirrorReconciler struct {
	mu        sync.Mutex
	known     map[string]string
	refreshed time.Time
}

func (m *MirrorReconciler) Sync(ctx context.Context, store *Store) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.known == nil || time.Since(m.refreshed) >= time.Minute {
		resources, err := store.Client.CoreV1().ConfigMaps(Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		known := make(map[string]string)
		for _, resource := range resources.Items {
			for _, key := range []string{"revision.json", "node.json", "operation.json"} {
				if value, ok := resource.Data[key]; ok {
					known[resource.Name] = value
				}
			}
		}
		m.known, m.refreshed = known, time.Now()
	}
	state, err := store.Load(ctx)
	if err != nil {
		return err
	}
	syncResource := func(name, label, key string, value any, immutable bool) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		serialized := string(data)
		if old, ok := m.known[name]; ok {
			if old == serialized {
				return nil
			}
			if immutable {
				return fmt.Errorf("immutable revision mirror %s does not match authoritative state", name)
			}
		}
		if immutable {
			yes := true
			_, err = store.Client.CoreV1().ConfigMaps(Namespace).Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{label: "true"}},
				Immutable:  &yes, Data: map[string]string{key: serialized},
			}, metav1.CreateOptions{})
		} else {
			err = store.upsertResource(ctx, name, label, key, serialized)
		}
		if err != nil {
			m.known = nil
			return err
		}
		m.known[name] = serialized
		return nil
	}
	for id, revision := range state.Revisions {
		if err := syncResource("skali-revision-"+id, RevisionLabel, "revision.json", revision, true); err != nil {
			return err
		}
	}
	for id, node := range state.Nodes {
		if err := syncResource("skali-node-"+id, NodeLabel, "node.json", node, false); err != nil {
			return err
		}
	}
	for id, op := range state.Operations {
		if err := syncResource("skali-operation-"+id, OperationLabel, "operation.json", op, false); err != nil {
			return err
		}
	}
	return nil
}
