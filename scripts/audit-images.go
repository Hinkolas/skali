//go:build ignore

// Run from the repository root: go run scripts/audit-images.go.
// The inventory reads the same pins/manifests as the installer; it does not
// resolve tags, pull images, or inspect an operator's existing installation.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Hinkolas/skali/internal/bundle"
	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

type image struct {
	Ref    string   `json:"ref"`
	Scopes []string `json:"scopes"`
	Source string   `json:"source"`
}

func main() {
	images := map[string]*image{}
	add := func(ref, scope, source string) {
		if old := images[ref]; old != nil {
			if !slices.Contains(old.Scopes, scope) {
				old.Scopes = append(old.Scopes, scope)
			}
			return
		}
		images[ref] = &image{ref, []string{scope}, source}
	}
	for _, ref := range localdev.RequiredImages() {
		add(ref, "runtime", "internal/localdev.RequiredImages")
	}
	add(localdev.K3sImage, "runtime", "internal/localdev.K3sImage (managed hosts install the matching k3s binary)")
	for _, major := range cnpg.SupportedMajors("postgres") {
		v, ok := cnpg.Lookup("postgres", major)
		if !ok {
			panic(fmt.Sprintf("missing postgres image for major %d", major))
		}
		add(v.Ref, "runtime", "internal/substrate/cnpg catalog")
	}
	// Include images passed in arguments/environment, not just Pod image fields:
	// cert-manager's ACME solver and Longhorn's engine/CSI/support images are
	// created later by their controllers.
	refPattern := regexp.MustCompile(`(?:quay\.io|ghcr\.io|docker\.io)/[a-zA-Z0-9_./-]+:[a-zA-Z0-9_.-]+`)
	for _, manifest := range []struct {
		data   []byte
		scope  string
		source string
	}{
		{bundle.CNPGManifest(), "runtime", "vendored CNPG manifest"},
		{bundle.CertManagerManifest(), "runtime", "vendored cert-manager manifest (including ACME solver)"},
		{bundle.LonghornManifest(), "longhorn", "vendored Longhorn manifest (including controller-created images)"},
	} {
		refs := refPattern.FindAllString(string(manifest.data), -1)
		if len(refs) == 0 {
			panic("no image references in " + manifest.source)
		}
		for _, ref := range refs {
			add(ref, manifest.scope, manifest.source)
		}
	}
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		return string(b)
	}
	for _, line := range strings.Split(read(".github/mirrored-images.txt"), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			add("docker.io/"+line, "build", ".github/mirrored-images.txt (same digests as GHCR copies)")
		}
	}
	paths := []string{"build/skalid.Dockerfile", "build/skalid.release.Dockerfile"}
	err := filepath.WalkDir("examples", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "Dockerfile" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
	for _, path := range paths {
		scope := "build"
		if strings.HasPrefix(path, "examples/") {
			scope = "examples"
		}
		for _, line := range strings.Split(read(path), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && strings.EqualFold(fields[0], "FROM") && fields[1] != "scratch" {
				if strings.HasPrefix(fields[1], "--") || strings.Contains(fields[1], "$") {
					panic("update inventory parser for FROM in " + path)
				}
				add(fields[1], scope, path)
			}
		}
	}
	result := make([]image, 0, len(images))
	for _, v := range images {
		result = append(result, *v)
	}
	slices.SortFunc(result, func(a, b image) int { return strings.Compare(a.Ref, b.Ref) })
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		panic(err)
	}
}
