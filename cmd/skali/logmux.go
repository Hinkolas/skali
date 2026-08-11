package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// logMux serializes labeled line streams onto one writer so child-process
// output and the cluster log follow interleave without tearing lines.
type logMux struct {
	mu  sync.Mutex
	out io.Writer
}

func newLogMux(out io.Writer) *logMux { return &logMux{out: out} }

// Writer returns a labeled stream. Writes are line-buffered: a partial line
// waits for its newline (or Flush) before appearing, so interleaved sources
// always emit whole lines. An empty label passes lines through bare (the
// cluster follow already prefixes pods).
func (m *logMux) Writer(label string) *logMuxWriter {
	return &logMuxWriter{mux: m, label: label}
}

func (m *logMux) writeLine(label, line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if label == "" {
		fmt.Fprintln(m.out, line)
		return
	}
	fmt.Fprintf(m.out, "%s%s\n", label, line)
}

type logMuxWriter struct {
	mux     *logMux
	label   string
	mu      sync.Mutex
	partial strings.Builder
}

func (w *logMuxWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			w.mux.writeLine(w.label, w.partial.String())
			w.partial.Reset()
			continue
		}
		w.partial.WriteByte(b)
	}
	return len(p), nil
}

// Flush emits a trailing partial line, for a source that ended mid-line.
func (w *logMuxWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.partial.Len() > 0 {
		w.mux.writeLine(w.label, w.partial.String())
		w.partial.Reset()
	}
}
