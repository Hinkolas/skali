package skill

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestInstallPreservesNestedAssetsAndPrunesObsoleteContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skali")
	content := fstest.MapFS{
		"SKILL.md":                   {Data: []byte(managedMarker)},
		"references/nested/guide.md": {Data: []byte("guide")},
		"references/old.md":          {Data: []byte("obsolete")},
		"old/nested/file.md":         {Data: []byte("obsolete directory")},
	}
	_, err := installContent(dir, content)
	require.NoError(t, err)
	delete(content, "references/old.md")
	delete(content, "old/nested/file.md")
	paths, err := installContent(dir, content)
	require.NoError(t, err)
	require.Len(t, paths, 2)
	data, err := os.ReadFile(filepath.Join(dir, "references/nested/guide.md"))
	require.NoError(t, err)
	require.Equal(t, "guide", string(data))
	require.NoFileExists(t, filepath.Join(dir, "references/old.md"))
	require.NoDirExists(t, filepath.Join(dir, "old"))
}
