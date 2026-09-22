package manifest

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ChangeKind classifies a manifest grammar change.
type ChangeKind string

const (
	ChangeAdded   ChangeKind = "added"
	ChangeRemoved ChangeKind = "removed"
	ChangeChanged ChangeKind = "changed"
)

// Change describes a grammar change. Revision comes from its filename, not
// from an application release. WhenOmitted matches default-only changes.
type Change struct {
	Revision    int        `yaml:"-"`
	Kind        ChangeKind `yaml:"kind"`
	Path        string     `yaml:"path"`
	Message     string     `yaml:"message"`
	Hint        string     `yaml:"hint"`
	WhenOmitted bool       `yaml:"whenOmitted,omitempty"`
}

//go:embed changes/*
var changeFiles embed.FS

// Ledger is the embedded, append-only history, oldest first.
var Ledger = mustLoadChanges()

func mustLoadChanges() []Change {
	ledger, err := loadChanges(changeFiles)
	if err != nil {
		panic(err)
	}
	return ledger
}

var changeFilename = regexp.MustCompile(`^([0-9]{5})_[a-z0-9]+(?:[-_][a-z0-9]+)*\.yaml$`)
var changePath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.(?:[A-Za-z_][A-Za-z0-9_]*|\*))*$`)

func loadChanges(files fs.FS) ([]Change, error) {
	names, err := fs.ReadDir(files, "changes")
	if err != nil {
		return nil, err
	}
	var ledger []Change
	for _, file := range names {
		match := changeFilename.FindStringSubmatch(file.Name())
		if file.IsDir() || match == nil {
			return nil, fmt.Errorf("invalid manifest change filename %q", file.Name())
		}
		revision, _ := strconv.Atoi(match[1])
		if revision != len(ledger)+1 {
			return nil, fmt.Errorf("manifest change %s: expected revision %d", file.Name(), len(ledger)+1)
		}
		data, err := fs.ReadFile(files, "changes/"+file.Name())
		if err != nil {
			return nil, err
		}
		var change Change
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&change); err != nil {
			return nil, fmt.Errorf("manifest change %s: %w", file.Name(), err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("manifest change %s: expected one YAML document", file.Name())
		}
		if (change.Kind != ChangeAdded && change.Kind != ChangeRemoved && change.Kind != ChangeChanged) ||
			!changePath.MatchString(change.Path) || strings.TrimSpace(change.Message) == "" || strings.TrimSpace(change.Hint) == "" ||
			(change.WhenOmitted && change.Kind != ChangeChanged) {
			return nil, fmt.Errorf("manifest change %s: invalid kind, path, message, hint, or whenOmitted", file.Name())
		}
		change.Revision = revision
		ledger = append(ledger, change)
	}
	if len(ledger) == 0 {
		return nil, fmt.Errorf("manifest change history is empty")
	}
	return ledger, nil
}

// CurrentRevision is the latest change understood by this compiler.
func CurrentRevision() int { return Ledger[len(Ledger)-1].Revision }

// Matches compares a concrete path; * stands for one collection key.
func (c Change) Matches(path string) bool {
	pattern, segments := strings.Split(c.Path, "."), strings.Split(path, ".")
	if len(pattern) != len(segments) {
		return false
	}
	for i := range pattern {
		if pattern[i] != "*" && pattern[i] != segments[i] {
			return false
		}
	}
	return true
}

func ChangesSince(revision int) []Change {
	var changes []Change
	for _, change := range Ledger {
		if change.Revision > revision {
			changes = append(changes, change)
		}
	}
	return changes
}

// Removed supplies actionable diagnostics even when no local history exists.
func Removed(path string) (Change, bool) { return removedIn(Ledger, path) }
func removedIn(ledger []Change, path string) (Change, bool) {
	for _, change := range ledger {
		if change.Kind == ChangeRemoved && change.Matches(path) {
			return change, true
		}
	}
	return Change{}, false
}
