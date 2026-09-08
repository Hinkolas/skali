package backup

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/sys/unix"
)

func clearDirectory(path string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "lost+found" {
			continue
		}
		if err := root.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func archiveName(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("invalid absolute or empty archive entry")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("archive entry traverses parent")
		}
	}
	name = filepath.Clean(filepath.FromSlash(name))
	if !filepath.IsLocal(name) || name == "lost+found" || strings.HasPrefix(name, "lost+found"+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry targets protected path")
	}
	return name, nil
}

// Kept as a lexical validation helper; extraction itself only uses os.Root.
func securePath(root, name string) (string, error) {
	name, err := archiveName(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

func archiveMode(mode int64) fs.FileMode {
	result := fs.FileMode(mode) & fs.ModePerm
	if mode&04000 != 0 {
		result |= fs.ModeSetuid
	}
	if mode&02000 != 0 {
		result |= fs.ModeSetgid
	}
	if mode&01000 != 0 {
		result |= fs.ModeSticky
	}
	return result
}

func restoreMetadata(root *os.Root, name string, h *tar.Header) error {
	if h.Uid < 0 || h.Gid < 0 {
		return fmt.Errorf("invalid numeric archive ownership")
	}
	if h.Typeflag == tar.TypeSymlink {
		if err := root.Lchown(name, h.Uid, h.Gid); err != nil {
			return err
		}
		// Parent directories cannot be symlinks: extractTar rejects that shape.
		parent, err := root.Open(filepath.Dir(name))
		if err != nil {
			return err
		}
		defer parent.Close()
		times := []unix.Timespec{unix.NsecToTimespec(h.ModTime.UnixNano()), unix.NsecToTimespec(h.ModTime.UnixNano())}
		return unix.UtimesNanoAt(int(parent.Fd()), filepath.Base(name), times, unix.AT_SYMLINK_NOFOLLOW)
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chown(h.Uid, h.Gid); err != nil {
		return err
	}
	if err := file.Chmod(archiveMode(h.Mode)); err != nil {
		return err
	}
	return root.Chtimes(name, h.ModTime, h.ModTime)
}

func extractTar(path string, in io.Reader) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	decompressor, err := zstd.NewReader(in)
	if err != nil {
		return err
	}
	defer decompressor.Close()
	archive := tar.NewReader(decompressor)
	entries := map[string]*tar.Header{}
	var directories, links []string
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name, err := archiveName(header.Name)
		if err != nil {
			return err
		}
		if _, ok := entries[name]; ok {
			return fmt.Errorf("duplicate archive entry %q", name)
		}
		if name == "." && header.Typeflag != tar.TypeDir {
			return fmt.Errorf("archive root must be a directory")
		}
		for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
			if h, ok := entries[parent]; ok && h.Typeflag != tar.TypeDir {
				return fmt.Errorf("archive entry beneath non-directory %q", parent)
			}
		}
		if header.Typeflag != tar.TypeDir {
			for prior := range entries {
				if strings.HasPrefix(prior, name+string(os.PathSeparator)) {
					return fmt.Errorf("archive entry conflicts with existing children")
				}
			}
		}
		copied := *header
		entries[name] = &copied
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0700); err != nil {
				return err
			}
			directories = append(directories, name)
		case tar.TypeReg, tar.TypeRegA:
			if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
				return err
			}
			file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, archive)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if err := restoreMetadata(root, name, header); err != nil {
				return fmt.Errorf("restore metadata %q: %w", name, err)
			}
		case tar.TypeSymlink:
			if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
				return err
			}
			links = append(links, name)
		default:
			return fmt.Errorf("unsupported archive entry type %d", header.Typeflag)
		}
	}
	for _, name := range links {
		header := entries[name]
		if err := root.Symlink(header.Linkname, name); err != nil {
			return err
		}
		if err := restoreMetadata(root, name, header); err != nil {
			return fmt.Errorf("restore symlink metadata: %w", err)
		}
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, name := range directories {
		if err := restoreMetadata(root, name, entries[name]); err != nil {
			return fmt.Errorf("restore directory metadata %q: %w", name, err)
		}
	}
	return nil
}
