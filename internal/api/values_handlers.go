package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/values"
	"github.com/Hinkolas/skali/internal/valuestore"
)

// valuesHandlers is the environment-values surface. Every value is a
// secret: the client submits one flat map, names the definition does not
// reference are skipped and reported (never an error), and stored values
// are write-only; listings carry names and versions alone. DELETE
// tombstones a value without touching the versions old revisions pinned.
type valuesHandlers struct {
	projects *project.Service
	values   *valuestore.Service
	st       *store.Store
}

type valueEntryPayload struct {
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

var valueNamePattern = regexp.MustCompile("^[A-Za-z_][A-Za-z0-9_]*$")

// GET /v1/environments/{id}/values
func (h *valuesHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	entries, err := h.values.Summary(r.Context(), id)
	if err != nil {
		writeValuesError(r.Context(), w, err)
		return
	}
	payload := make([]valueEntryPayload, len(entries))
	for i, entry := range entries {
		payload[i] = valueEntryPayload{
			Name:    entry.Name,
			Version: entry.Version,
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Values []valueEntryPayload `json:"values"`
	}{payload})
}

// PUT /v1/environments/{id}/values
func (h *valuesHandlers) put(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Values map[string]string `json:"values"`
		// DefinitionVersionID validates names against a submitted candidate
		// definition instead of the project draft, so a deploy can stage
		// values for the exact manifest it is about to promote.
		DefinitionVersionID string `json:"definition_version_id"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	if len(req.Values) == 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "values is required and must not be empty")
		return
	}

	env, err := h.projects.GetEnvironment(r.Context(), id)
	if err != nil {
		writeProjectError(r.Context(), w, err)
		return
	}
	requirements, ok := h.requirements(w, r, env.ProjectID, req.DefinitionVersionID)
	if !ok {
		return
	}

	// Unknown names are skipped and reported, never an error: the store only
	// accepts what the definition references, and an empty string is a real
	// value that stages like any other.
	accepted, _, skipped := values.Conform(requirements, req.Values)

	candidate, err := h.values.Stage(r.Context(), id, accepted)
	if err != nil {
		writeValuesError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		CandidateID string   `json:"candidate_id"`
		Staged      []string `json:"staged"`
		Skipped     []string `json:"skipped"`
	}{
		CandidateID: candidate.ID.String(),
		Staged:      nonNil(candidate.Names),
		Skipped:     nonNil(skipped),
	})
}

// DELETE /v1/environments/{id}/values/{name}
//
// Tombstones the current value: future deployments no longer include the
// name, while revisions that pinned earlier versions keep resolving. A name
// with no current value is 404, so typos surface instead of succeeding
// silently.
func (h *valuesHandlers) del(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	name, err := url.PathUnescape(chi.URLParam(r, "name"))
	if err != nil || !valueNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, codeBadRequest, "the value name is not a valid environment variable name")
		return
	}
	unset, err := h.values.Unset(r.Context(), id, []string{name})
	if err != nil {
		writeValuesError(r.Context(), w, err)
		return
	}
	if unset == 0 {
		writeError(w, http.StatusNotFound, codeNotFound, "no stored value named "+name)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requirements resolves the variable requirements the submitted values are
// validated against: an explicitly named candidate definition version, or
// the project draft.
func (h *valuesHandlers) requirements(w http.ResponseWriter, r *http.Request, projectID uuid.UUID, definitionVersion string) ([]compiler.VariableRequirement, bool) {
	if definitionVersion != "" {
		versionID, err := uuid.Parse(definitionVersion)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, "definition_version_id must be a UUID")
			return nil, false
		}
		row, err := h.st.GetDefinitionVersionByID(r.Context(), versionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, codeNotFound, "the definition version does not exist")
				return nil, false
			}
			writeInternalError(r.Context(), w, "get definition version", err)
			return nil, false
		}
		if row.ProjectID != projectID {
			writeError(w, http.StatusUnprocessableEntity, codeBadRequest,
				"the definition version belongs to another project")
			return nil, false
		}
		var definition compiler.ProjectDefinition
		if err := json.Unmarshal(row.Definition, &definition); err != nil {
			writeInternalError(r.Context(), w, "decode definition", err)
			return nil, false
		}
		return definition.RequiredVariables, true
	}

	draft, err := h.projects.GetDraft(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, project.ErrDraftNotFound) {
			// Without any definition every submitted name would be skipped
			// silently, which is a worse trap than a 409.
			writeError(w, http.StatusConflict, codeConflict,
				"the project has no draft yet: submit a manifest first so the value contract is known")
			return nil, false
		}
		writeProjectError(r.Context(), w, err)
		return nil, false
	}
	return draft.Definition.RequiredVariables, true
}

// writeValuesError maps valuestore sentinel errors onto the envelope.
func writeValuesError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, valuestore.ErrEnvironmentNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, valuestore.ErrStagingConflict):
		writeError(w, http.StatusConflict, codeConflict, "a concurrent submission staged these values; retry")
	default:
		writeInternalError(ctx, w, "values error", err)
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
