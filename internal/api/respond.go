package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

const maxBodyBytes = 1 << 20 // 1 MiB; auth payloads are tiny

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("api: encode response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// decodeJSON strictly parses the request body into v: unknown fields, trailing
// data, and bodies over maxBodyBytes are errors. The returned error message is
// safe to echo to the client.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %s", err)
	}
	if dec.More() {
		return fmt.Errorf("invalid JSON body: unexpected trailing data")
	}
	return nil
}
