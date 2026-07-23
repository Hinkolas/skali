// Package checkout persists the per-checkout deploy-target binding in
// .skali/target.yaml under the project root: the remote master URL, the
// project name, and the default environment. It is disposable local tool
// state and stores no secrets; deleting the directory relinks the checkout
// on the next plan or deploy.
package checkout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Target is the on-disk shape of .skali/target.yaml.
type Target struct {
	Master      string `yaml:"master"`
	Project     string `yaml:"project"`
	Environment string `yaml:"environment"`
}

// header explains the file to whoever opens it; Save writes it above the
// marshaled fields.
const header = "# Skali checkout binding: where this checkout deploys.\n" +
	"# Local tool state, safe to delete; the next plan or deploy relinks.\n"

// Dir returns the checkout's local state directory.
func Dir(root string) string {
	return filepath.Join(root, ".skali")
}

// Path returns the binding file location under the project root.
func Path(root string) string {
	return filepath.Join(Dir(root), "target.yaml")
}

// Load reads the binding; (nil, nil) when none exists. A corrupt or
// incomplete file is an error naming the fix.
func Load(root string) (*Target, error) {
	path := Path(root)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checkout: read %s: %w", path, err)
	}
	var target Target
	if err := yaml.Unmarshal(data, &target); err != nil {
		return nil, fmt.Errorf("checkout: parse %s: %v; delete the file to relink this checkout", path, err)
	}
	if target.Master == "" || target.Project == "" || target.Environment == "" {
		return nil, fmt.Errorf("checkout: %s is incomplete (master, project, and environment are required); delete the file to relink this checkout", path)
	}
	return &target, nil
}

// Save writes the binding and makes .skali/ self-ignoring on first write by
// dropping a .gitignore containing "*"; an existing .gitignore is never
// overwritten, and the user's own .gitignore is never touched.
func Save(root string, target *Target) error {
	dir := Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("checkout: mkdir %s: %w", dir, err)
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
			return fmt.Errorf("checkout: write %s: %w", ignore, err)
		}
	}
	data, err := yaml.Marshal(target)
	if err != nil {
		return fmt.Errorf("checkout: marshal: %w", err)
	}
	path := Path(root)
	if err := os.WriteFile(path, append([]byte(header), data...), 0o644); err != nil {
		return fmt.Errorf("checkout: write %s: %w", path, err)
	}
	return nil
}
