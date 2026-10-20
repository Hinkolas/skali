package devremote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Hinkolas/skali/internal/filelock"
)

// Session is one kind of long-running process this package runs on a
// machine: the proxy on the laptop, expose on the remote machine. At most
// one of each runs, guarded by a lock the process holds for its lifetime,
// so a crash releases it and nothing stale can claim to be running.
type Session struct {
	dir  string
	name string
}

// The two sessions.
const (
	sessionProxy  = "proxy"
	sessionExpose = "expose"
)

// ProxySession and ExposeSession name this machine's two sessions.
func ProxySession() (Session, error)  { return newSession(sessionProxy) }
func ExposeSession() (Session, error) { return newSession(sessionExpose) }

func newSession(name string) (Session, error) {
	dir, err := Dir()
	if err != nil {
		return Session{}, err
	}
	return Session{dir: dir, name: name}, nil
}

// Name is the session's kind, as the CLI calls it.
func (s Session) Name() string { return s.name }

// LogPath is where a background process of this session writes its
// output; each start truncates it.
func (s Session) LogPath() string { return filepath.Join(s.dir, s.name+".log") }

func (s Session) lockPath() string   { return filepath.Join(s.dir, s.name+".lock") }
func (s Session) recordPath() string { return filepath.Join(s.dir, s.name+".json") }

// Record is what a session remembers. The target outlives the process,
// so a bare skali dev proxy restarts with the last host and CA; PID and
// Started describe the process that last held the lock and only mean
// something while it still does.
type Record struct {
	// Host is the proxy's target host, or the address expose listens on.
	Host string `json:"host"`
	// Fingerprint is the pinned CA's fingerprint (proxy only).
	Fingerprint string    `json:"fingerprint,omitempty"`
	PID         int       `json:"pid,omitempty"`
	Started     time.Time `json:"started,omitzero"`
}

// ErrRunning reports that another process holds the session.
var ErrRunning = errors.New("devremote: session already running")

// Acquire claims the session for this process until release is called or
// the process exits.
func (s Session) Acquire() (release func(), err error) {
	release, err = filelock.Try(s.lockPath())
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, ErrRunning
	}
	return release, nil
}

// Running reports whether a process holds the session, with its record
// when one does.
func (s Session) Running() (*Record, bool, error) {
	if _, err := os.Stat(s.lockPath()); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	release, err := filelock.Try(s.lockPath())
	if err != nil {
		return nil, false, err
	}
	if release != nil {
		release()
		return nil, false, nil
	}
	record, err := s.Load()
	if err != nil {
		// The holder writes its record right after it locks; between
		// the two it is running all the same.
		return &Record{}, true, nil
	}
	return record, true, nil
}

// Load reads the session's record; os.ErrNotExist when there is none.
func (s Session) Load() (*Record, error) {
	raw, err := os.ReadFile(s.recordPath())
	if err != nil {
		return nil, err
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("devremote: read %s: %w", s.recordPath(), err)
	}
	return &record, nil
}

// Save writes the session's record.
func (s Session) Save(record Record) error {
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.recordPath(), append(raw, '\n'), 0o600)
}

// writeFileAtomic replaces path with data, creating its directory.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
