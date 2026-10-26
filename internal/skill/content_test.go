package skill_test

import (
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/skill"
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
	readme, err := fs.ReadFile(os.DirFS("../.."), "README.md")
	require.NoError(t, err)
	found["README.md"] = string(readme)
	err = fs.WalkDir(skill.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
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

// Complete manifests pass the published editor schema as well as the
// compiler, the two checks a reader's own manifest meets.
func TestSkillCompleteManifestsCompile(t *testing.T) {
	t.Parallel()

	schema := resolvedSchema(t)
	found := 0
	for name, text := range documents(t) {
		for index, match := range completeManifestBlock.FindAllStringSubmatch(text, -1) {
			found++
			document, err := manifest.Parse([]byte(match[1]), name)
			require.NoError(t, err, "%s block %d", name, index)
			_, err = compiler.Compile(document)
			require.NoError(t, err, "%s block %d", name, index)
			var value any
			require.NoError(t, yaml.Unmarshal([]byte(match[1]), &value), "%s block %d", name, index)
			require.NoError(t, schema.Validate(value), "%s block %d: schema", name, index)
		}
	}
	require.GreaterOrEqual(t, found, 2, "no ```yaml manifest blocks found; the marker convention drifted")
}

// uncompiledFields lists manifest fields the reference documents in prose
// only, each with the reason no complete manifest shows it. Every other
// field must appear in a compiled example, so a new field cannot ship
// without one.
var uncompiledFields = map[string]string{
	"buckets.*.versioning":                              "valid vocabulary, rejected at deploy",
	"buckets.*.lifecycle.expireNoncurrentVersionsAfter": "valid vocabulary, rejected at deploy",
	"buckets.*.quotas.objects":                          "valid vocabulary, rejected at deploy",
	"buckets.*.quotas.maxObjectSize":                    "valid vocabulary, rejected at deploy",
	"databases.*.recovery":                              "recorded but not acted on yet",
	"databases.*.recovery.pointInTime":                  "recorded but not acted on yet",
}

// Every field of the manifest schema appears in at least one complete
// manifest of the manifest reference, so each is compiled and checked
// against the schema by TestSkillCompleteManifestsCompile.
func TestManifestReferenceCoversEveryField(t *testing.T) {
	t.Parallel()

	schema, err := manifest.Schema()
	require.NoError(t, err)
	fields := map[string]bool{}
	schemaFields(schema, "", fields)

	reference, ok := skill.Reference("manifest")
	require.True(t, ok)
	used := map[string]bool{}
	for _, match := range completeManifestBlock.FindAllStringSubmatch(string(reference), -1) {
		var value any
		require.NoError(t, yaml.Unmarshal([]byte(match[1]), &value))
		usedFields(schema, value, "", used)
	}

	var missing []string
	for field := range fields {
		if reason, excused := uncompiledFields[field]; excused {
			require.False(t, used[field], "%s is listed in uncompiledFields (%s) but a complete manifest uses it; remove it from the list", field, reason)
			continue
		}
		if !used[field] {
			missing = append(missing, field)
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing, "fields in no ```yaml manifest block of the manifest reference")
	for field := range uncompiledFields {
		require.True(t, fields[field], "uncompiledFields lists %s, which is not a manifest field", field)
	}
}

// Examples exercise syntax, but they cannot explain an option. Require a
// dedicated prose row for every key (including containers), and reject stale
// rows naming fields that no longer exist.
func TestManifestReferenceExplainsEveryField(t *testing.T) {
	t.Parallel()
	schema, err := manifest.Schema()
	require.NoError(t, err)
	fields := map[string]bool{}
	schemaFields(schema, "", fields)
	reference, ok := skill.Reference("manifest")
	require.True(t, ok)
	_, referenceText, found := strings.Cut(string(reference), "## Field reference\n")
	require.True(t, found)
	referenceText, _, _ = strings.Cut(referenceText, "## Examples\n")
	rows := regexp.MustCompile("(?m)^\\| `([^`]+)` \\| (.+) \\|$")
	explained := map[string]bool{}
	for _, row := range rows.FindAllStringSubmatch(referenceText, -1) {
		require.True(t, fields[row[1]], "field reference describes unknown key %s", row[1])
		require.False(t, explained[row[1]], "duplicate field explanation: %s", row[1])
		require.NotEmpty(t, strings.TrimSpace(row[2]), "%s needs an explanation", row[1])
		explained[row[1]] = true
	}
	for field := range fields {
		require.True(t, explained[field], "%s needs a prose row in the field reference", field)
	}
}

// schemaFields collects every property path, writing a collection key
// (a map entry) as "*".
func schemaFields(schema *jsonschema.Schema, prefix string, into map[string]bool) {
	for name, property := range schema.Properties {
		path := joinField(prefix, name)
		into[path] = true
		schemaFields(property, path, into)
	}
	if schema.AdditionalProperties != nil {
		schemaFields(schema.AdditionalProperties, joinField(prefix, "*"), into)
	}
	if schema.Items != nil {
		schemaFields(schema.Items, prefix, into)
	}
}

// usedFields collects the property paths a YAML value sets, walking the
// schema alongside it to tell fields from collection keys.
func usedFields(schema *jsonschema.Schema, value any, prefix string, into map[string]bool) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if property, ok := schema.Properties[key]; ok {
				path := joinField(prefix, key)
				into[path] = true
				usedFields(property, child, path, into)
			} else if schema.AdditionalProperties != nil {
				usedFields(schema.AdditionalProperties, child, joinField(prefix, "*"), into)
			}
		}
	case []any:
		if schema.Items != nil {
			for _, item := range value {
				usedFields(schema.Items, item, prefix, into)
			}
		}
	}
}

func joinField(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func resolvedSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	schema, err := manifest.Schema()
	require.NoError(t, err)
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	require.NoError(t, err)
	return resolved
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
