// Package examples checks every checked-in example against the current
// manifest grammar, so an example cannot silently drift from what the
// compiler accepts. The check is offline: no cluster, no builds, no
// network.
package examples

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/values"
)

// expectedFailures lists examples that are meant not to pass the check,
// each with the reason: an intentional negative example, or one that needs
// an input this offline check cannot provide. A listed example must still
// fail, so a fixed one cannot stay excused. Every other directory here is an
// example and must pass.
var expectedFailures = map[string]string{}

// TestExamples is the inventory: every directory under examples/ is
// checked, so a new example is covered the moment it is added.
func TestExamples(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	seen := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "testdata" {
			continue
		}
		name := entry.Name()
		seen[name] = true
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := checkExample(name)
			if reason, excused := expectedFailures[name]; excused {
				require.Error(t, err, "listed as an expected failure (%s) but passes; remove it from expectedFailures", reason)
				return
			}
			require.NoError(t, err)
		})
	}
	for name := range expectedFailures {
		require.True(t, seen[name], "expectedFailures lists %s, which is not an example directory", name)
	}
}

// A manifest below an example's root would be skipped by the inventory,
// which only looks at each example's root.
func TestNoNestedManifests(t *testing.T) {
	t.Parallel()
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && path == "testdata" {
			return filepath.SkipDir
		}
		if !entry.IsDir() && isManifestName(entry.Name()) && strings.Count(filepath.ToSlash(path), "/") != 1 {
			return fmt.Errorf("%s: manifests must sit at an example's root to be checked", path)
		}
		return nil
	})
	require.NoError(t, err)
}

// The regression fixture: invalid grammar fails the same check the
// examples pass.
func TestCheckRejectsInvalidGrammar(t *testing.T) {
	t.Parallel()
	err := checkExample(filepath.Join("testdata", "invalid-grammar"))
	require.ErrorContains(t, err, "applications.web.ressources: unknown field")
}

// checkExample runs an example directory through the offline pipeline of
// skali validate and skali compile --target kubernetes: parsing, the editor
// schema, validation and compilation, value resolution, and rendering.
//
// .env.example supplies the project values: deterministic placeholders,
// never real secrets (.env stays local and is never read here). It must set
// every value the manifest requires and nothing it does not reference.
// Build applications render with a placeholder image, since nothing is
// built.
func checkExample(directory string) error {
	path, err := manifestPath(directory)
	if err != nil {
		return err
	}
	document, err := manifest.ParseFile(path)
	if err != nil {
		return err
	}
	if err := checkSchema(path); err != nil {
		return err
	}
	result, err := compiler.Compile(document)
	if err != nil {
		return err
	}

	provided := map[string]string{}
	envExample := filepath.Join(directory, ".env.example")
	if file, err := values.ParseFile(envExample); err == nil {
		provided = file.Values
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, missing, unreferenced := values.Conform(result.Definition.RequiredVariables, provided)
	if len(missing) > 0 {
		return fmt.Errorf("%s: missing placeholders for required values: %s", envExample, strings.Join(missing, ", "))
	}
	if len(unreferenced) > 0 {
		return fmt.Errorf("%s: keys not referenced by the manifest: %s", envExample, strings.Join(unreferenced, ", "))
	}

	images := make(map[string]string)
	for key, application := range result.Definition.Applications {
		if application.Source.Kind == "build" {
			images[key] = "registry.invalid/" + result.Definition.Name + "/" + key + ":example"
		}
	}
	_, err = kubernetes.Render(result, kubernetes.Options{
		Namespace:   "skali-" + result.Definition.Name,
		Variables:   provided,
		BuildImages: images,
	})
	return err
}

// manifestPath finds the manifest at an example's root by the names manifest
// discovery accepts.
func manifestPath(directory string) (string, error) {
	var found []string
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() && isManifestName(entry.Name()) {
			found = append(found, filepath.Join(directory, entry.Name()))
		}
	}
	sort.Strings(found)
	switch len(found) {
	case 0:
		return "", fmt.Errorf("%s: no skali.yml or skali.yaml; every directory under examples/ is an example", directory)
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("%s: more than one manifest: %s", directory, strings.Join(found, ", "))
	}
}

func isManifestName(name string) bool {
	return name == "skali.yml" || name == "skali.yaml"
}

// checkSchema validates against the published JSON schema, the one the
// examples point editors at.
func checkSchema(path string) error {
	schema, err := manifest.Schema()
	if err != nil {
		return err
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := resolved.Validate(value); err != nil {
		return fmt.Errorf("%s: schema: %w", path, err)
	}
	return nil
}
