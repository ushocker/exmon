// Package version holds exmon's version, set when the release is built:
//
//	go build -ldflags "-X exmon/internal/version.Version=1.4.0" .
//
// -ldflags "-X package.Variable=value" asks the Go linker to overwrite a string
// variable's initial value, so the version is baked into the exe without editing
// code. A plain `go build` or `go run` leaves it as "dev".
package version

// Version is the running build's version: semantic versioning ("1.4.0"), or "dev".
var Version = "dev"
