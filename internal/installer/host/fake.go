package host

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
)

// Fake is the scripted test double: an in-memory filesystem, a handler
// table keyed by command name, and an ordered log of every mutation for
// assertions. The zero value is usable.
type Fake struct {
	// FS maps cleaned paths to file contents. Directories exist implicitly
	// through their children and explicitly through MkdirAll entries
	// (marked by a nil value under the path itself).
	FS    map[string][]byte
	Modes map[string]fs.FileMode
	// Handlers dispatches Run by Command.Name; a missing handler fails the
	// call loudly so tests declare every command they expect.
	Handlers map[string]func(Command) (Result, error)
	// Commands records every Run in order.
	Commands []Command
	// Writes records every mutating filesystem call in order, as
	// "op path" strings.
	Writes []string
}

func (f *Fake) init() {
	if f.FS == nil {
		f.FS = map[string][]byte{}
	}
	if f.Modes == nil {
		f.Modes = map[string]fs.FileMode{}
	}
}

func (f *Fake) Run(_ context.Context, cmd Command) (Result, error) {
	f.Commands = append(f.Commands, cmd)
	handler, ok := f.Handlers[cmd.Name]
	if !ok {
		return Result{}, fmt.Errorf("host fake: no handler for command %q", cmd.Name)
	}
	result, err := handler(cmd)
	if cmd.Stdout != nil && result.Stdout != "" {
		io.WriteString(cmd.Stdout, result.Stdout)
		result.Stdout = ""
	}
	if cmd.Stderr != nil && result.Stderr != "" {
		io.WriteString(cmd.Stderr, result.Stderr)
		result.Stderr = ""
	}
	return result, err
}

func (f *Fake) ReadFile(_ context.Context, path string) ([]byte, error) {
	f.init()
	data, ok := f.FS[cleanPath(path)]
	if !ok || data == nil {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (f *Fake) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	f.init()
	path = cleanPath(path)
	f.FS[path] = append([]byte(nil), data...)
	f.Modes[path] = perm
	f.Writes = append(f.Writes, "write "+path)
	return nil
}

func (f *Fake) MkdirAll(_ context.Context, path string, perm fs.FileMode) error {
	f.init()
	path = cleanPath(path)
	if _, ok := f.FS[path]; !ok {
		f.FS[path] = nil
	}
	f.Modes[path] = perm | fs.ModeDir
	f.Writes = append(f.Writes, "mkdir "+path)
	return nil
}

func (f *Fake) Remove(_ context.Context, path string) error {
	f.init()
	path = cleanPath(path)
	for candidate := range f.FS {
		if candidate == path || strings.HasPrefix(candidate, path+"/") {
			delete(f.FS, candidate)
			delete(f.Modes, candidate)
		}
	}
	f.Writes = append(f.Writes, "remove "+path)
	return nil
}

func (f *Fake) Stat(_ context.Context, path string) (Info, error) {
	f.init()
	path = cleanPath(path)
	if data, ok := f.FS[path]; ok {
		if data == nil {
			return Info{Exists: true, Mode: f.Modes[path]}, nil
		}
		return Info{Exists: true, Mode: f.Modes[path], Size: int64(len(data))}, nil
	}
	// Implicit directory: any child makes the path exist.
	for candidate := range f.FS {
		if strings.HasPrefix(candidate, path+"/") {
			return Info{Exists: true, Mode: fs.ModeDir}, nil
		}
	}
	return Info{}, nil
}

// Paths lists every stored path in sorted order, for assertions.
func (f *Fake) Paths() []string {
	f.init()
	paths := make([]string, 0, len(f.FS))
	for path := range f.FS {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
