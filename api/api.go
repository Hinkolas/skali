// Package api holds the hand-written OpenAPI contract for the skali API.
// Nothing is generated from it: the handlers in internal/api, the CLI client
// in internal/client, and the Studio types in studio/src/lib/types mirror it
// by hand, so handler changes must be reflected here.
package api

import _ "embed"

//go:embed openapi.yaml
var OpenAPI []byte
