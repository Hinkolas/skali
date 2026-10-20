package api

import "net/http"

// DevCAPath is where a local platform serves its development CA
// certificate, below the /api prefix the platform host routes to skalid:
// https://skali.localhost/api/dev/ca.crt.
const DevCAPath = "/dev/ca.crt"

// devCAHandler serves the development CA certificate as PEM. The response
// is not cached: a reset installation answers with a new CA at the same
// URL.
func devCAHandler(certPEM string) http.HandlerFunc {
	body := []byte(certPEM)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}
}
