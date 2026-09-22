package checkout

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestReviewStateLifecycle(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "skali.yml")
	_, known, err := Review(path)
	require.NoError(t, err)
	require.False(t, known)
	require.NoDirExists(t, Dir(root))
	got, err := SaveReview(path, 3, true)
	require.NoError(t, err)
	require.Equal(t, 3, got)
	got, err = SaveReview(path, 5, true)
	require.NoError(t, err)
	require.Equal(t, 3, got)
	got, err = SaveReview(path, 2, false)
	require.NoError(t, err)
	require.Equal(t, 3, got)
	got, err = SaveReview(path, 5, false)
	require.NoError(t, err)
	require.Equal(t, 5, got)
	_, err = SaveReview(filepath.Join(root, "other.yaml"), 1, false)
	require.NoError(t, err)
	got, known, err = Review(path)
	require.NoError(t, err)
	require.True(t, known)
	require.Equal(t, 5, got)
	ignore, err := os.ReadFile(filepath.Join(Dir(root), ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, "*\n", string(ignore))
	require.NoError(t, os.WriteFile(filepath.Join(Dir(root), ".gitignore"), []byte("*\n!target.yaml\n"), 0600))
	require.NoError(t, Save(root, &Target{Master: "https://example.test", Project: "demo", Environment: "prod"}))
	_, err = SaveReview(path, 6, false)
	require.NoError(t, err)
	target, err := Load(root)
	require.NoError(t, err)
	require.Equal(t, "demo", target.Project)
	ignore, err = os.ReadFile(filepath.Join(Dir(root), ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, "*\n!target.yaml\n", string(ignore))
}

func TestReviewStateRejectsCorruption(t *testing.T) {
	for _, data := range []string{"schemaVersion: 1\nmanifests:\n  skali.yml: 1.5\n", "schemaVersion: 1\nmanifests:\n  skali.yml: '3'\n", "", "[", "schemaVersion: 2\nmanifests: {}\n", "schemaVersion: 1\n", "schemaVersion: 1\nmanifests:\n  skali.yml: -1\n", "schemaVersion: 1\nmanifests:\n  ../skali.yml: 1\n", "schemaVersion: 1\nmanifests: {}\nunknown: true\n", "schemaVersion: 1\nmanifests: {}\n---\n{}", "schemaVersion: 1\nmanifests:\n  skali.yml: null\n"} {
		t.Run(fmt.Sprintf("%q", data), func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, EnsureDir(root))
			file := filepath.Join(Dir(root), reviewFilename)
			require.NoError(t, os.WriteFile(file, []byte(data), 0600))
			_, _, err := Review(filepath.Join(root, "skali.yml"))
			require.Error(t, err)
			_, err = SaveReview(filepath.Join(root, "skali.yml"), 5, false)
			require.Error(t, err)
			after, err := os.ReadFile(file)
			require.NoError(t, err)
			require.Equal(t, data, string(after))
		})
	}
}

func TestConcurrentReviewUpdates(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 1; i <= 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := SaveReview(filepath.Join(root, "shared.yml"), i, false)
			errs <- err
			_, err = SaveReview(filepath.Join(root, fmt.Sprintf("%d.yml", i)), i, false)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	revision, _, err := Review(filepath.Join(root, "shared.yml"))
	require.NoError(t, err)
	require.Equal(t, 20, revision)
	state, err := loadReviews(root)
	require.NoError(t, err)
	require.Len(t, state.Manifests, 21)
}
