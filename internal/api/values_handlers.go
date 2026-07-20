package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/valuestore"
	"github.com/Hinkolas/skali/internal/values"
)

// valuesHandlers is the environment-values surface. Secrecy is declared by
// the manifest values block alone: the client submits one flat map and the
// server separates plain from secret against the project's current draft
// definition. Secret values are write-only.
type valuesHandlers struct {
	projects *project.Service
	values   *valuestore.Service
}

type valueEntryPayload struct {
	Name    string `json:"name"`
	Secret  bool   `json:"secret"`
	Version int64  `json:"version"`
	Value   string `json:"value,omitempty"`
}

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
			Secret:  entry.Secret,
			Version: entry.Version,
			Value:   entry.Value,
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
	draft, err := h.projects.GetDraft(r.Context(), env.ProjectID)
	if err != nil {
		if errors.Is(err, project.ErrDraftNotFound) {
			writeError(w, http.StatusConflict, codeConflict,
				"the project has no draft yet: submit a manifest first so value secrecy is known")
			return
		}
		writeProjectError(r.Context(), w, err)
		return
	}

	resolved, unknown := splitBySecrecy(draft.Definition.RequiredVariables, req.Values)
	if len(unknown) > 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest,
			"values not declared by the project definition: "+strings.Join(unknown, ", "))
		return
	}

	candidate, err := h.values.Stage(r.Context(), id, resolved)
	if err != nil {
		writeValuesError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		CandidateID string   `json:"candidate_id"`
		Plain       []string `json:"plain"`
		Secret      []string `json:"secret"`
	}{
		CandidateID: candidate.ID.String(),
		Plain:       nonNil(candidate.Plain),
		Secret:      nonNil(candidate.Secret),
	})
}

// splitBySecrecy classifies a flat name -> value map against the compiled
// definition's variable requirements. Empty values follow the dotenv rule
// (empty = unset) and are dropped. Returns the unknown names sorted.
func splitBySecrecy(requirements []compiler.VariableRequirement, submitted map[string]string) (values.Resolved, []string) {
	known := make(map[string]bool, len(requirements))
	secret := make(map[string]bool, len(requirements))
	for _, requirement := range requirements {
		known[requirement.Name] = true
		secret[requirement.Name] = requirement.Secret
	}
	resolved := values.Resolved{
		Plain:  make(map[string]string),
		Secret: make(map[string]string),
	}
	var unknown []string
	for name, value := range submitted {
		if !known[name] {
			unknown = append(unknown, name)
			continue
		}
		if value == "" {
			continue
		}
		if secret[name] {
			resolved.Secret[name] = value
		} else {
			resolved.Plain[name] = value
		}
	}
	sort.Strings(unknown)
	return resolved, unknown
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
