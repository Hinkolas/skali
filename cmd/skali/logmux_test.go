package main

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogMuxLineBufferingAndPrefixes(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	mux := newLogMux(&out)
	web := mux.Writer("web | ")
	bare := mux.Writer("")

	// A partial write stays buffered until its newline arrives.
	_, err := web.Write([]byte("hello "))
	require.NoError(t, err)
	require.Empty(t, out.String())
	_, err = web.Write([]byte("world\nsecond"))
	require.NoError(t, err)
	require.Equal(t, "web | hello world\n", out.String())

	// Bare lines pass through unlabeled.
	_, err = bare.Write([]byte("pod-1  cluster line\n"))
	require.NoError(t, err)
	require.Contains(t, out.String(), "pod-1  cluster line\n")

	// Flush emits the trailing partial line; a second flush is a no-op.
	web.Flush()
	web.Flush()
	require.Contains(t, out.String(), "web | second\n")
}

func TestLogMuxConcurrentWholeLines(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	mux := newLogMux(&out)
	var wg sync.WaitGroup
	for _, label := range []string{"a | ", "b | ", "c | "} {
		wg.Add(1)
		go func(label string) {
			defer wg.Done()
			writer := mux.Writer(label)
			for range 50 {
				// Byte-wise writes stress the buffering.
				for _, b := range []byte(label + "line") {
					_, _ = writer.Write([]byte{b})
				}
				_, _ = writer.Write([]byte("\n"))
			}
		}(label)
	}
	wg.Wait()
	for line := range strings.SplitSeq(strings.TrimSuffix(out.String(), "\n"), "\n") {
		require.Regexp(t, `^([abc] \| ){2}line$`, line, "torn line: %q", line)
	}
}

func TestDevApplicationsExtraction(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "skali.yml", `version: "1"
name: devdemo
applications:
  web:
    image: example.invalid/web:1
    ports:
      web:
        port: 3000
    dev:
      command: [bun, run, dev]
      ports:
        web: 5173
  worker:
    image: example.invalid/worker:1
`)
	project, err := loadLocalProject(filepath.Join(root, "skali.yml"))
	require.NoError(t, err)
	devApps := devApplications(project)
	require.Len(t, devApps, 1)
	require.Equal(t, []string{"bun", "run", "dev"}, devApps["web"].Command)
	require.Equal(t, map[string]int{"web": 5173}, devApps["web"].Ports)
}
