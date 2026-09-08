// Package diagnostic contains non-fatal, user-visible operation diagnostics.
package diagnostic

type Warning struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Paths   []string `json:"paths"`
}
