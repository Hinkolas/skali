package build

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Hinkolas/skali/internal/utils"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

// IgnoreFile is the explicit ignore file honored in the context root, so
// collection excludes what a plain docker build would exclude.
const IgnoreFile = ".dockerignore"

// ErrContextEscape: a context path or symlink reaches outside its allowed
// root. The compiler already rejects escaping manifest paths; this guards
// the filesystem reality (symlinks, races) at collection time.
var ErrContextEscape = errors.New("build: path escapes the build context")

// hardExcludedDirs are never collected or descended into, regardless of
// ignore rules: VCS internals and skali's own state directory.
var hardExcludedDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".skali": true,
}

// Context is one collected build context: the deterministic file inventory
// and its tree hash. The hash covers relative path, file kind, the
// executable bit, content, and symlink targets, so any input change and
// nothing else changes it.
type Context struct {
	// Dir is the absolute context root.
	Dir string
	// Files lists the included paths, slash-separated and relative to Dir,
	// in walk order (lexical).
	Files []string
	// TreeHash is the hex sha256 over the file inventory.
	TreeHash string
}

type CollectOptions struct {
	// ExcludeFiles are absolute paths excluded regardless of ignore rules;
	// the selected environment file goes here. Env files named .env or
	// .env.* are always excluded everywhere.
	ExcludeFiles []string
}

// Collect walks the build context below projectRoot deterministically,
// applying the ignore file, the hard safety exclusions, and the symlink
// containment rules, and returns the inventory with its tree hash.
func Collect(projectRoot, contextRel string, opts CollectOptions) (*Context, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("build: resolve project root: %w", err)
	}
	dir := filepath.Join(root, contextRel)
	if rel, err := filepath.Rel(root, dir); err != nil || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: context %s", ErrContextEscape, contextRel)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("build: context %s: %w", contextRel, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("build: context %s is not a directory", contextRel)
	}

	matcher, err := loadIgnore(dir)
	if err != nil {
		return nil, err
	}
	excluded := make(map[string]bool, len(opts.ExcludeFiles))
	for _, path := range opts.ExcludeFiles {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("build: resolve excluded file %s: %w", path, err)
		}
		excluded[abs] = true
	}

	collected := &Context{Dir: dir}
	tree := sha256.New()
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("build: relativize %s: %w", path, err)
		}
		rel = filepath.ToSlash(rel)

		if entry.IsDir() {
			if hardExcludedDirs[entry.Name()] {
				return filepath.SkipDir
			}
			if matcher != nil {
				matched, err := matcher.MatchesOrParentMatches(rel)
				if err != nil {
					return fmt.Errorf("build: match %s: %w", rel, err)
				}
				// A matched directory can only be skipped wholesale when
				// no exception pattern could re-include a child.
				if matched && !matcher.Exclusions() {
					return filepath.SkipDir
				}
			}
			return nil
		}

		if isEnvFileName(entry.Name()) || excluded[path] {
			return nil
		}
		if matcher != nil {
			matched, err := matcher.MatchesOrParentMatches(rel)
			if err != nil {
				return fmt.Errorf("build: match %s: %w", rel, err)
			}
			if matched {
				return nil
			}
		}

		switch entry.Type() {
		case 0: // regular file
			mode, err := entry.Info()
			if err != nil {
				return fmt.Errorf("build: stat %s: %w", rel, err)
			}
			contentHash, _, err := utils.FileSHA256(path)
			if err != nil {
				return fmt.Errorf("build: %w", err)
			}
			executable := "-"
			if mode.Mode()&0o111 != 0 {
				executable = "x"
			}
			fmt.Fprintf(tree, "%s\x00f%s\x00%s\n", rel, executable, contentHash)
		case fs.ModeSymlink:
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("build: readlink %s: %w", rel, err)
			}
			if filepath.IsAbs(target) {
				return fmt.Errorf("%w: symlink %s targets absolute %s", ErrContextEscape, rel, target)
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), target))
			if inside, err := filepath.Rel(dir, resolved); err != nil || inside == ".." ||
				strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				return fmt.Errorf("%w: symlink %s targets %s", ErrContextEscape, rel, target)
			}
			fmt.Fprintf(tree, "%s\x00l\x00%s\n", rel, filepath.ToSlash(target))
		default:
			return fmt.Errorf("build: %s has unsupported file type %s", rel, entry.Type())
		}
		collected.Files = append(collected.Files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	collected.TreeHash = hex.EncodeToString(tree.Sum(nil))
	return collected, nil
}

// ConfigHash folds the build configuration into one hash: the Dockerfile
// content, the target stage, and the plain arguments. Secret inputs never
// enter any hash.
func ConfigHash(dockerfile []byte, target string, arguments map[string]string) string {
	h := sha256.New()
	h.Write(dockerfile)
	fmt.Fprintf(h, "\x00%s\x00", target)
	names := make([]string, 0, len(arguments))
	for name := range arguments {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(h, "%s=%s\n", name, arguments[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// InputHash is the artifact dedup key: an unchanged tree, configuration,
// and platform reuse the verified artifact instead of rebuilding.
func InputHash(treeHash, configHash, platform string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s", treeHash, configHash, platform)
	return hex.EncodeToString(h.Sum(nil))
}

func loadIgnore(dir string) (*patternmatcher.PatternMatcher, error) {
	file, err := os.Open(filepath.Join(dir, IgnoreFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("build: open %s: %w", IgnoreFile, err)
	}
	defer file.Close()
	patterns, err := ignorefile.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("build: read %s: %w", IgnoreFile, err)
	}
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, fmt.Errorf("build: parse %s: %w", IgnoreFile, err)
	}
	return matcher, nil
}

func isEnvFileName(name string) bool {
	return name == ".env" || strings.HasPrefix(name, ".env.")
}
