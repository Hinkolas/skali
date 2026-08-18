package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/dbstore"
)

// databasesHandlers serves database-service connection projections. The
// connection endpoint reads durable rows only; credential reveal is the one
// sanctioned request-time cluster read and sits behind
// fresh authentication. Credential values are returned once and never
// journaled or logged.
type databasesHandlers struct {
	db *dbstore.Service
	// secrets reads one Secret's data; nil without a cluster (API-only),
	// which disables reveal.
	secrets func(ctx context.Context, namespace, name string) (map[string][]byte, error)
}

type databaseConnectionPayload struct {
	Service           string `json:"service"`
	Phase             string `json:"phase"`
	Engine            string `json:"engine"`
	Major             int32  `json:"major"`
	Isolation         string `json:"isolation"`
	Availability      string `json:"availability"`
	Host              string `json:"host,omitempty"`
	Port              int32  `json:"port,omitempty"`
	Database          string `json:"database,omitempty"`
	CredentialVersion int64  `json:"credential_version,omitempty"`
}

type databaseCredentialsPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
	URL      string `json:"url"`
}

func (h *databasesHandlers) connection(w http.ResponseWriter, r *http.Request) {
	environmentID, ok := pathID(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	row, err := h.db.LiveServiceClaim(r.Context(), environmentID, key)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "no live database claim for "+key)
			return
		}
		writeError(w, http.StatusInternalServerError, codeInternal, "loading the claim failed")
		return
	}
	payload := databaseConnectionPayload{
		Service:      "databases." + key,
		Phase:        row.Phase,
		Engine:       row.Engine,
		Major:        row.Major,
		Isolation:    row.Isolation,
		Availability: row.Availability,
	}
	if tenant, err := h.db.LiveTenant(r.Context(), row.ID); err == nil {
		payload.Host = tenant.Host
		payload.Port = tenant.Port
		payload.Database = tenant.DatabaseName
		payload.CredentialVersion = tenant.CredentialVersion
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *databasesHandlers) reveal(w http.ResponseWriter, r *http.Request) {
	environmentID, ok := pathID(w, r)
	if !ok {
		return
	}
	if h.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "no cluster available for credential reveal")
		return
	}
	key := chi.URLParam(r, "key")
	row, err := h.db.LiveServiceClaim(r.Context(), environmentID, key)
	if err != nil {
		if errors.Is(err, dbstore.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "no live database claim for "+key)
			return
		}
		writeError(w, http.StatusInternalServerError, codeInternal, "loading the claim failed")
		return
	}
	if claim.Phase(row.Phase) != claim.PhaseProvisioned {
		writeError(w, http.StatusConflict, codeConflict, "the database is not provisioned yet")
		return
	}
	tenant, err := h.db.LiveTenant(r.Context(), row.ID)
	if err != nil {
		writeError(w, http.StatusConflict, codeConflict, "the database has no live tenant")
		return
	}
	data, err := h.secrets(r.Context(), "skali-platform", tenant.CredentialSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "reading the credential failed")
		return
	}
	username := string(data["username"])
	password := string(data["password"])
	writeJSON(w, http.StatusOK, databaseCredentialsPayload{
		Username: username,
		Password: password,
		URL: "postgresql://" + username + ":" + password + "@" +
			tenant.Host + ":" + strconv.Itoa(int(tenant.Port)) + "/" + tenant.DatabaseName,
	})
}
