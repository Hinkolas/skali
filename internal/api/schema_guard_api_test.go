package api

import (
	"context"
	"encoding/json"
	"fmt"
	versionpkg "github.com/Hinkolas/skali/internal/version"
	"net/http"
	"strings"
	"testing"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/revision"

	"github.com/stretchr/testify/require"
)

// corruptRevisionSchema rewrites a stored revision as if an incompatible
// build had written it: foreign schema column and document stamp.
func (a *testAPI) corruptRevisionSchema(t *testing.T, revisionID string) {
	t.Helper()
	_, err := a.st.Pool.Exec(context.Background(),
		`UPDATE revisions SET schema_version = '99',
		 document = jsonb_set(document, '{schema}', '99')
		 WHERE id = $1`, revisionID)
	require.NoError(t, err)
}

// The previous reader unmarshaled the schemaVersion/version aliases and
// required the string "1". Exercise those checks against actual new writes,
// then read the same rows through the new readers without changing a byte.
func TestNewStorageRowsRemainReadableDuringRolloutOverlap(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("overlap@example.com", "hunter2hunter2")
	token := a.login("overlap@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)
	definitionID := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionID, "overlap-value")
	id := a.deployAndActivate(t, token, envID, definitionID, candidate)
	ctx := context.Background()
	var revisionBytes, definitionBytes []byte
	var revisionSchema, definitionSchema, checksum, hash string
	require.NoError(t, a.st.Pool.QueryRow(ctx, `SELECT r.schema_version,d.schema_version,r.document,d.definition,r.checksum,d.definition_hash FROM revisions r JOIN definition_versions d ON d.id=r.definition_version_id WHERE r.id=$1`, id).Scan(&revisionSchema, &definitionSchema, &revisionBytes, &definitionBytes, &checksum, &hash))
	require.Equal(t, "1", revisionSchema)
	require.Equal(t, "1", definitionSchema)
	var oldDefinition struct {
		Version string `json:"version"`
		compiler.ProjectDefinition
	}
	var oldRevision struct {
		SchemaVersion string `json:"schemaVersion"`
		Definition    struct {
			Version string `json:"version"`
			compiler.ProjectDefinition
		} `json:"definition"`
		Checksum string `json:"checksum"`
	}
	require.NoError(t, json.Unmarshal(definitionBytes, &oldDefinition))
	require.NoError(t, json.Unmarshal(revisionBytes, &oldRevision))
	require.Equal(t, "1", oldDefinition.Version)
	require.Equal(t, "1", oldRevision.SchemaVersion)
	require.Equal(t, "1", oldRevision.Definition.Version)
	require.Equal(t, checksum, oldRevision.Checksum)
	require.Equal(t, oldDefinition.Name, oldRevision.Definition.Name)
	current, err := revision.Decode(revisionBytes)
	require.NoError(t, err)
	definition, err := compiler.DecodeDefinition(definitionBytes)
	require.NoError(t, err)
	require.Equal(t, revision.Schema, current.Schema)
	require.Equal(t, compiler.DefinitionSchema, current.Definition.Schema)
	require.Equal(t, compiler.DefinitionSchema, definition.Schema)
	require.Equal(t, hash, current.DefinitionHash)
	require.Equal(t, checksum, current.Checksum)
	var after []byte
	require.NoError(t, a.st.Pool.QueryRow(ctx, "SELECT document FROM revisions WHERE id=$1", id).Scan(&after))
	require.JSONEq(t, string(revisionBytes), string(after))
}

// legacyEnvelope rewrites a stored definition and revision the way releases
// up to v0.1.0-rc.2 wrote them: the manifest version "1" in place of the
// schema integer, same shape otherwise.
func (a *testAPI) legacyEnvelope(t *testing.T, revisionID string) {
	t.Helper()
	ctx := context.Background()
	var document, definition []byte
	var definitionVersionID string
	require.NoError(t, a.st.Pool.QueryRow(ctx,
		`SELECT document, definition_version_id FROM revisions WHERE id = $1`, revisionID).Scan(&document, &definitionVersionID))
	require.NoError(t, a.st.Pool.QueryRow(ctx,
		`SELECT definition FROM definition_versions WHERE id = $1`, definitionVersionID).Scan(&definition))

	toLegacy := func(data []byte, schemaKey, legacyKey string) []byte {
		var object map[string]any
		require.NoError(t, json.Unmarshal(data, &object))
		require.Contains(t, object, schemaKey)
		delete(object, schemaKey)
		object[legacyKey] = "1"
		out, err := json.Marshal(object)
		require.NoError(t, err)
		return out
	}
	var revisionDocument map[string]any
	require.NoError(t, json.Unmarshal(toLegacy(document, "schema", "schemaVersion"), &revisionDocument))
	nested, err := json.Marshal(revisionDocument["definition"])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(toLegacy(nested, "schema", "version"), new(map[string]any)))
	var legacyDefinition map[string]any
	require.NoError(t, json.Unmarshal(toLegacy(nested, "schema", "version"), &legacyDefinition))
	revisionDocument["definition"] = legacyDefinition
	rewritten, err := json.Marshal(revisionDocument)
	require.NoError(t, err)

	_, err = a.st.Pool.Exec(ctx, `UPDATE revisions SET document = $2 WHERE id = $1`, revisionID, string(rewritten))
	require.NoError(t, err)
	_, err = a.st.Pool.Exec(ctx, `UPDATE definition_versions SET definition = $2 WHERE id = $1`,
		definitionVersionID, string(toLegacy(definition, "schema", "version")))
	require.NoError(t, err)
}

// Stored documents written under another schema version fail loudly with
// unsupported_schema instead of opaque 500s, on every read and pointer-move
// surface.
func TestStaleSchemaFailsLoudly(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("schema@example.com", "hunter2hunter2")
	token := a.login("schema@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "schema-one-value")
	first := a.deployAndActivate(t, token, envID, definitionVersion, candidate)
	candidate = a.stageValues(t, token, envID, definitionVersion, "schema-two-value")
	second := a.deployAndActivate(t, token, envID, definitionVersion, candidate)
	_ = projectID

	// A stale non-target revision: reading it and targeting it both refuse.
	a.corruptRevisionSchema(t, first)
	status, body := a.do("GET", "/v1/revisions/"+first, token, nil)
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "unsupported_schema", errCode(body))
	status, body = a.do("PUT", "/v1/environments/"+envID+"/target", token,
		map[string]any{"revision_id": first})
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "unsupported_schema", errCode(body))
	require.Contains(t, errMessage(body), "redeploy the environment")

	// The healthy target keeps the status endpoint working.
	status, body = a.do("GET", "/v1/environments/"+envID+"/status", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)

	// A stale target revision turns status into a clear 409, never a 500.
	a.corruptRevisionSchema(t, second)
	status, body = a.do("GET", "/v1/environments/"+envID+"/status", token, nil)
	require.Equal(t, http.StatusConflict, status, "%v", body)
	require.Equal(t, "unsupported_schema", errCode(body))
}

// Documents written before the schema split keep decoding after the
// platform upgrade: reads, status, and the draft all answer 200.
func TestLegacyDocumentsStillDecode(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("legacy@example.com", "hunter2hunter2")
	token := a.login("legacy@example.com", "hunter2hunter2")
	projectID, envID := a.createEnvironment(t, token)

	definitionVersion := a.submitDefinition(t, token, projectID, deployAPIManifest)
	candidate := a.stageValues(t, token, envID, definitionVersion, "legacy-value")
	revisionID := a.deployAndActivate(t, token, envID, definitionVersion, candidate)
	a.legacyEnvelope(t, revisionID)

	status, body := a.do("GET", "/v1/revisions/"+revisionID, token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = a.do("GET", "/v1/environments/"+envID+"/status", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = a.do("GET", "/v1/projects/"+projectID+"/draft", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = a.do("GET", "/v1/projects", token, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
}

func TestServerRejectsNewerManifestReview(t *testing.T) {
	a := newTestAPI(t)
	old := versionpkg.Version
	versionpkg.Version = "v0.4.0"
	t.Cleanup(func() { versionpkg.Version = old })
	a.createUser("review@example.com", "hunter2hunter2")
	token := a.login("review@example.com", "hunter2hunter2")
	projectID, _ := a.createEnvironment(t, token)
	source := strings.Replace(deployAPIManifest, "skali: v0.1.0-rc.3", "skali: v0.5.0", 1)
	status, body := a.do("POST", "/v1/projects/"+projectID+"/definitions", token, map[string]any{"source": source})
	require.Equal(t, http.StatusUnprocessableEntity, status, "%v", body)
	require.Contains(t, fmt.Sprint(body), "v0.5.0")
	require.Contains(t, fmt.Sprint(body), "v0.4.0")
}
