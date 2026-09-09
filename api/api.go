// Package api holds the hand-written OpenAPI contract for the skali API. The
// spec is the source clients are generated from (openapi-typescript for the
// web console); handler changes in internal/api must be reflected here.
package api

import _ "embed"

//go:embed openapi.yaml
var OpenAPI []byte
