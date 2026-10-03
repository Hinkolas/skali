package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Hinkolas/skali/internal/rotation"
	"github.com/Hinkolas/skali/internal/substrate"
)

// Bounds of a rotation's overlap window: below a minute the previous
// credentials would be revoked before any consumer rolled; above seven
// days nothing legitimately holds them (the SigV4 presign maximum for a
// bucket; a database's open sessions have no such horizon, the same cap
// keeps the two surfaces alike).
const (
	defaultRetireAfter = time.Hour
	minRetireAfter     = time.Minute
	maxRetireAfter     = 7 * 24 * time.Hour
)

// rotate serves POST .../{collection}/{key}/credentials/rotate: a new
// credential as a journaled run of kind rotation. The consumers roll onto
// it and the previous one retires after the overlap window (a bucket's
// signed URLs and a database's open sessions fail from then on), so the
// routes sit behind sudo mode. The body is optional.
func rotate(ctl *rotation.Controller, collection string, w http.ResponseWriter, r *http.Request) {
	environmentID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		RetireAfterSeconds *int64 `json:"retire_after_seconds"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}
	retireAfter := defaultRetireAfter
	if req.RetireAfterSeconds != nil {
		retireAfter = time.Duration(*req.RetireAfterSeconds) * time.Second
		if retireAfter < minRetireAfter || retireAfter > maxRetireAfter {
			writeError(w, http.StatusBadRequest, codeBadRequest,
				"retire_after_seconds must be between 60 (one minute) and 604800 (seven days)")
			return
		}
	}
	key := chi.URLParam(r, "key")
	runID, err := ctl.Create(r.Context(), rotation.Input{
		Collection:    collection,
		EnvironmentID: environmentID,
		ServiceKey:    key,
		RetireAfter:   retireAfter,
		Actor:         UserFrom(r.Context()).ID.String(),
	})
	if err != nil {
		writeRotationError(w, key, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		RunID string `json:"run_id"`
	}{runID.String()})
}

func writeRotationError(w http.ResponseWriter, key string, err error) {
	switch {
	case errors.Is(err, rotation.ErrEnvironmentNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "environment not found")
	case errors.Is(err, rotation.ErrBucketNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "no live bucket claim for "+key)
	case errors.Is(err, rotation.ErrDatabaseNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "no live database claim for "+key)
	case errors.Is(err, substrate.ErrBucketNotProvisioned):
		writeError(w, http.StatusConflict, codeBucketNotProvisioned, "the bucket is not provisioned yet")
	case errors.Is(err, substrate.ErrDatabaseNotProvisioned):
		writeError(w, http.StatusConflict, codeDatabaseNotProvisioned, "the database is not provisioned yet")
	case errors.Is(err, substrate.ErrBucketFenced):
		writeError(w, http.StatusConflict, codeBucketFenced,
			"a restore holds the bucket; rotate once it has finished")
	case errors.Is(err, substrate.ErrRotationInFlight):
		writeError(w, http.StatusConflict, codeRotationInFlight,
			"the previous credentials are still retiring; rotate again once they have")
	case errors.Is(err, rotation.ErrRunInFlight):
		writeError(w, http.StatusConflict, codeRunInFlight,
			"another run is in flight for this environment; wait for it or cancel it")
	default:
		writeError(w, http.StatusInternalServerError, codeInternal, "starting the rotation failed")
	}
}
