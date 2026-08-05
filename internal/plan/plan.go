// Package plan classifies the difference between an environment's active
// revision and a candidate revision into product-level changes. A plan is an
// explanation and a gate: destructive changes must be visible before a deploy
// is confirmed. Values never appear as plaintext; the plan carries names and
// change kinds only.
package plan

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/utils"
)

type Action string

const (
	ActionCreate Action = "create"
	ActionUpdate Action = "update"
	ActionRemove Action = "remove"
)

type Change struct {
	Service     string `json:"service"`
	Action      Action `json:"action"`
	Destructive bool   `json:"destructive,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type ValueChange struct {
	Name   string `json:"name"`
	Action Action `json:"action"`
}

type Plan struct {
	Project string        `json:"project"`
	Changes []Change      `json:"changes,omitempty"`
	Values  []ValueChange `json:"values,omitempty"`
}

func (p *Plan) Empty() bool {
	return len(p.Changes) == 0 && len(p.Values) == 0
}

func (p *Plan) Destructive() bool {
	for _, change := range p.Changes {
		if change.Destructive {
			return true
		}
	}
	return false
}

// Diff compares the active revision with a candidate. A nil active revision
// plans an initial deployment where everything is created.
func Diff(active, candidate *revision.Revision) *Plan {
	result := &Plan{Project: candidate.Project}

	result.diffApplications(active, candidate)
	result.diffCollection(active, candidate, "databases",
		func(r *revision.Revision) map[string]json.RawMessage { return rawSpecs(r.Definition.Databases) },
		"deletes the logical database and its data")
	result.diffCollection(active, candidate, "buckets",
		func(r *revision.Revision) map[string]json.RawMessage { return rawSpecs(r.Definition.Buckets) },
		"deletes the bucket and its objects")
	result.diffCollection(active, candidate, "backups",
		func(r *revision.Revision) map[string]json.RawMessage { return rawSpecs(r.Definition.Backups) },
		"")
	result.diffValues(active, candidate)

	sort.SliceStable(result.Changes, func(i, j int) bool {
		return result.Changes[i].Service < result.Changes[j].Service
	})
	return result
}

func (p *Plan) diffApplications(active, candidate *revision.Revision) {
	activeSpecs := map[string]json.RawMessage{}
	if active != nil {
		activeSpecs = rawSpecs(active.Definition.Applications)
	}
	candidateSpecs := rawSpecs(candidate.Definition.Applications)

	for _, key := range unionKeys(activeSpecs, candidateSpecs) {
		service := "applications." + key
		activeSpec, inActive := activeSpecs[key]
		candidateSpec, inCandidate := candidateSpecs[key]
		switch {
		case !inActive:
			p.Changes = append(p.Changes, Change{Service: service, Action: ActionCreate})
		case !inCandidate:
			change := Change{Service: service, Action: ActionRemove}
			if len(active.Definition.Applications[key].Volumes) > 0 {
				change.Destructive = true
				change.Detail = "deletes persistent volumes"
			}
			p.Changes = append(p.Changes, change)
		default:
			change := Change{Service: service, Action: ActionUpdate}
			var reasons []string
			if string(activeSpec) != string(candidateSpec) {
				reasons = append(reasons, "configuration changed")
				if removed := removedVolumes(active, candidate, key); len(removed) > 0 {
					change.Destructive = true
					reasons = append(reasons, "deletes persistent volumes: "+strings.Join(removed, ", "))
				}
			}
			if active.Artifacts[key] != candidate.Artifacts[key] {
				reasons = append(reasons, artifactReason(active.Artifacts[key], candidate.Artifacts[key]))
			}
			if len(reasons) == 0 {
				continue
			}
			change.Detail = strings.Join(reasons, "; ")
			p.Changes = append(p.Changes, change)
		}
	}
}

// diffCollection compares one keyed service collection. A non-empty
// removeDetail marks removals of this collection as destructive.
func (p *Plan) diffCollection(active, candidate *revision.Revision, collection string,
	specs func(*revision.Revision) map[string]json.RawMessage, removeDetail string) {
	activeSpecs := map[string]json.RawMessage{}
	if active != nil {
		activeSpecs = specs(active)
	}
	candidateSpecs := specs(candidate)

	for _, key := range unionKeys(activeSpecs, candidateSpecs) {
		service := collection + "." + key
		activeSpec, inActive := activeSpecs[key]
		candidateSpec, inCandidate := candidateSpecs[key]
		switch {
		case !inActive:
			p.Changes = append(p.Changes, Change{Service: service, Action: ActionCreate})
		case !inCandidate:
			p.Changes = append(p.Changes, Change{
				Service:     service,
				Action:      ActionRemove,
				Destructive: removeDetail != "",
				Detail:      removeDetail,
			})
		case string(activeSpec) != string(candidateSpec):
			p.Changes = append(p.Changes, Change{Service: service, Action: ActionUpdate, Detail: "configuration changed"})
		}
	}
}

// diffValues compares value references by version; plaintext never enters a
// revision, so a value change is always a version change.
func (p *Plan) diffValues(active, candidate *revision.Revision) {
	activeSecrets := map[string]revision.SecretRef{}
	if active != nil {
		activeSecrets = active.Secrets
	}

	for _, name := range unionKeys(activeSecrets, candidate.Secrets) {
		activeRef, inActive := activeSecrets[name]
		candidateRef, inCandidate := candidate.Secrets[name]
		switch {
		case !inActive:
			p.Values = append(p.Values, ValueChange{Name: name, Action: ActionCreate})
		case !inCandidate:
			p.Values = append(p.Values, ValueChange{Name: name, Action: ActionRemove})
		case activeRef.Version != candidateRef.Version:
			p.Values = append(p.Values, ValueChange{Name: name, Action: ActionUpdate})
		}
	}
	sort.SliceStable(p.Values, func(i, j int) bool { return p.Values[i].Name < p.Values[j].Name })
}

// artifactReason explains an artifact swap with git-style short digests. A
// candidate whose build or import has not run yet carries the pending
// sentinel instead of a digest, so there is no hash to show.
func artifactReason(active, candidate revision.Artifact) string {
	if candidate.Digest == revision.PendingDigest {
		return "a new artifact replaces " + utils.ShortChecksum(active.Digest)
	}
	return fmt.Sprintf("artifact %s replaces %s",
		utils.ShortChecksum(candidate.Digest), utils.ShortChecksum(active.Digest))
}

func removedVolumes(active, candidate *revision.Revision, key string) []string {
	var removed []string
	candidateVolumes := candidate.Definition.Applications[key].Volumes
	for name := range active.Definition.Applications[key].Volumes {
		if _, kept := candidateVolumes[name]; !kept {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	return removed
}

// rawSpecs canonicalizes every entry of a keyed collection for comparison.
func rawSpecs[T any](collection map[string]T) map[string]json.RawMessage {
	specs := make(map[string]json.RawMessage, len(collection))
	for key, spec := range collection {
		data, err := json.Marshal(spec)
		if err != nil {
			panic(fmt.Sprintf("encode service spec: %v", err))
		}
		specs[key] = data
	}
	return specs
}

func unionKeys[A, B any](first map[string]A, second map[string]B) []string {
	seen := make(map[string]bool, len(first)+len(second))
	keys := make([]string, 0, len(first)+len(second))
	for key := range first {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	for key := range second {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
