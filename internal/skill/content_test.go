package skill_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/skill"
	"github.com/stretchr/testify/require"
)

// completeManifestBlock matches fenced blocks marked as complete manifests
// with the "yaml manifest" info string. Plain "yaml" fences are fragments
// and are never compiled.
var completeManifestBlock = regexp.MustCompile("(?ms)^```yaml manifest\n(.*?)^```$")

func TestSkillCompleteManifestsCompile(t *testing.T) {
	t.Parallel()

	found := 0
	err := fs.WalkDir(skill.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		require.NoError(t, err)
		if entry.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := fs.ReadFile(skill.FS(), path)
		require.NoError(t, err)
		for index, match := range completeManifestBlock.FindAllStringSubmatch(string(data), -1) {
			found++
			document, err := manifest.Parse([]byte(match[1]), path)
			require.NoError(t, err, "%s block %d", path, index)
			_, err = compiler.Compile(document)
			require.NoError(t, err, "%s block %d", path, index)
		}
		return nil
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, found, 2, "no ```yaml manifest blocks found; the marker convention drifted")
}

func TestSkillPointersResolve(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(skill.FS(), "SKILL.md")
	require.NoError(t, err)

	mentions := regexp.MustCompile("`([a-z-]+\\.md)`").FindAllStringSubmatch(string(data), -1)
	require.NotEmpty(t, mentions, "SKILL.md names no reference files")
	for _, mention := range mentions {
		_, err := fs.Stat(skill.FS(), mention[1])
		require.NoError(t, err, "SKILL.md points at %s", mention[1])
	}
}
