package kube

import (
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReplicasFieldPath addresses Deployment.spec.replicas in FieldsV1 form,
// the one field whose ownership moves between skalid and the autoscaler.
const ReplicasFieldPath = "f:spec.f:replicas"

// OwnsField reports whether the manager's server-side-apply entry contains
// the dot-separated FieldsV1 path (for example "f:spec.f:replicas"). Keys
// containing dots are not addressable; the paths this package needs never
// contain them. Pure: fixture-testable without a cluster.
func OwnsField(entries []metav1.ManagedFieldsEntry, manager, path string) bool {
	for _, entry := range entries {
		if entry.Manager != manager || entry.Operation != metav1.ManagedFieldsOperationApply {
			continue
		}
		fields, err := decodeFields(entry)
		if err != nil {
			return false
		}
		if containsPath(fields, strings.Split(path, ".")) {
			return true
		}
	}
	return false
}

// RemoveOwnedFields returns a copy of the entries with the given paths
// removed from the manager's Apply entry. Emptied ancestor keys are pruned
// so releasing one child never mutates into atomic ownership of its parent.
// The second return reports whether anything changed.
func RemoveOwnedFields(entries []metav1.ManagedFieldsEntry, manager string, paths ...string) ([]metav1.ManagedFieldsEntry, bool, error) {
	rewritten := make([]metav1.ManagedFieldsEntry, len(entries))
	copy(rewritten, entries)
	changed := false
	for index := range rewritten {
		entry := &rewritten[index]
		if entry.Manager != manager || entry.Operation != metav1.ManagedFieldsOperationApply {
			continue
		}
		fields, err := decodeFields(*entry)
		if err != nil {
			return nil, false, err
		}
		for _, path := range paths {
			if removePath(fields, strings.Split(path, ".")) {
				changed = true
			}
		}
		if !changed {
			continue
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return nil, false, fmt.Errorf("encode fields of %s: %w", manager, err)
		}
		rewrittenFields := &metav1.FieldsV1{}
		rewrittenFields.SetRawBytes(raw)
		entry.FieldsV1 = rewrittenFields
	}
	return rewritten, changed, nil
}

func decodeFields(entry metav1.ManagedFieldsEntry) (map[string]any, error) {
	if entry.FieldsV1 == nil {
		return map[string]any{}, nil
	}
	fields := map[string]any{}
	if err := json.Unmarshal(entry.FieldsV1.GetRawBytes(), &fields); err != nil {
		return nil, fmt.Errorf("decode fields of %s: %w", entry.Manager, err)
	}
	return fields, nil
}

func containsPath(fields map[string]any, segments []string) bool {
	current := fields
	for index, segment := range segments {
		value, present := current[segment]
		if !present {
			return false
		}
		if index == len(segments)-1 {
			return true
		}
		next, ok := value.(map[string]any)
		if !ok {
			return false
		}
		current = next
	}
	return false
}

func removePath(fields map[string]any, segments []string) bool {
	if len(segments) == 0 {
		return false
	}
	if len(segments) == 1 {
		if _, present := fields[segments[0]]; !present {
			return false
		}
		delete(fields, segments[0])
		return true
	}
	nested, ok := fields[segments[0]].(map[string]any)
	if !ok {
		return false
	}
	removed := removePath(nested, segments[1:])
	if removed && len(nested) == 0 {
		delete(fields, segments[0])
	}
	return removed
}

// UpgradeCreateEntry turns the manager's Update entry (what a Create
// records) into its Apply entry when the manager has no Apply entry yet, so
// later server-side applies by the same manager own the created fields
// instead of conflicting with a second owner of the same name. The second
// return reports whether anything changed.
func UpgradeCreateEntry(entries []metav1.ManagedFieldsEntry, manager string) ([]metav1.ManagedFieldsEntry, bool) {
	for _, entry := range entries {
		if entry.Manager == manager && entry.Operation == metav1.ManagedFieldsOperationApply {
			return entries, false
		}
	}
	rewritten := make([]metav1.ManagedFieldsEntry, len(entries))
	copy(rewritten, entries)
	changed := false
	for index := range rewritten {
		entry := &rewritten[index]
		if entry.Manager == manager && entry.Operation == metav1.ManagedFieldsOperationUpdate {
			entry.Operation = metav1.ManagedFieldsOperationApply
			changed = true
		}
	}
	return rewritten, changed
}
