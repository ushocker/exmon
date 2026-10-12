// Package senders registers every remote target type that lives outside the
// remote package. It exists so that remote doesn't import its target types (they
// import remote, and Go forbids import cycles).
//
// Add a new target type here, with one Register line.
package senders

import (
	"sync"

	"exmon/internal/remote"
	"exmon/internal/remote/azuremonitor"
)

var once sync.Once

// RegisterAll registers the target types. Calling it more than once is harmless:
// sync.Once runs the function only the first time.
func RegisterAll() {
	once.Do(func() {
		remote.Register(azuremonitor.TypeName, azuremonitor.Type)
	})
}
