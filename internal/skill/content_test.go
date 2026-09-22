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

// releaseTag matches anything that looks like a skali release, which the
// installed set must not carry: it is written once and outlives releases.
var releaseTag = regexp.MustCompile(`v\d+\.\d+\.\d+`)

// documents lists every embedded markdown file with its content: the
// installed set by path, the references by topic name.
func documents(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := fs.WalkDir(skill.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		require.NoError(t, err)
		if entry.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := fs.ReadFile(skill.FS(), path)
		require.NoError(t, err)
		found["skill/"+path] = string(data)
		return nil
	})
	require.NoError(t, err)
	for _, topic := range skill.Topics() {
		data, ok := skill.Reference(topic.Name)
		require.True(t, ok, topic.Name)
		found["reference/"+topic.Name] = string(data)
	}
	return found
}

func TestSkillCompleteManifestsCompile(t *testing.T) {
	t.Parallel()

	found := 0
	for name, text := range documents(t) {
		for index, match := range completeManifestBlock.FindAllStringSubmatch(text, -1) {
			found++
			document, err := manifest.Parse([]byte(match[1]), name)
			require.NoError(t, err, "%s block %d", name, index)
			_, err = compiler.Compile(document)
			require.NoError(t, err, "%s block %d", name, index)
		}
	}
	require.GreaterOrEqual(t, found, 2, "no ```yaml manifest blocks found; the marker convention drifted")
}

// The installed set is version-neutral: no complete manifest (which would
// contain release-specific grammar) and no release tag anywhere. Everything bound to a
// release is served by skali skill read instead.
func TestInstalledSetIsVersionNeutral(t *testing.T) {
	t.Parallel()

	for name, text := range documents(t) {
		if !strings.HasPrefix(name, "skill/") {
			continue
		}
		require.Empty(t, completeManifestBlock.FindAllString(text, -1), "%s carries a complete manifest", name)
		require.Empty(t, releaseTag.FindAllString(text, -1), "%s names a release", name)
	}
}

func TestSkillPointersResolve(t *testing.T) {
	t.Parallel()

	data, err := fs.ReadFile(skill.FS(), "SKILL.md")
	require.NoError(t, err)

	for _, topic := range skill.Topics() {
		require.Contains(t, string(data), "skali skill read "+topic.Name, "SKILL.md must tell the agent how to read the %s reference", topic.Name)
	}
}

// Every reference file is a topic and every topic has a file.
func TestTopicsMatchReferenceFiles(t *testing.T) {
	t.Parallel()

	names := skill.TopicNames()
	require.Equal(t, []string{"manifest", "cli", "architecture"}, names)
	for _, name := range names {
		content, ok := skill.Reference(name)
		require.True(t, ok, name)
		require.True(t, strings.HasPrefix(string(content), "# "), "%s must start with a title", name)
	}
	_, ok := skill.Reference("architecture")
	require.True(t, ok, "architecture is served by the selected release")
	_, ok = skill.Reference("../skill/SKILL")
	require.False(t, ok)
}
