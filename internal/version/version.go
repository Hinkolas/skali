// Package version carries the build version stamped at link time. It lives in
// its own package (not cmd/) so daemon internals — heartbeats, enrollment —
// can report it without importing a main package.
package version

// Version is overridden by the release build:
//
//	go build -ldflags "-X github.com/Hinkolas/skali/internal/version.Version=v0.1.0"
var Version = "v0.0.0-dev"
