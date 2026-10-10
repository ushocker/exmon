//go:build exmondev

// Only compiled into test builds (`go build -tags exmondev`); see trial.go.

package updater

// devNeverHealthy makes a test build that never reports itself healthy, so the
// helper's rollback can be tested on demand:
//
//	go build -tags exmondev -ldflags "-X exmon/internal/version.Version=0.1.2 -X exmon/internal/updater.devNeverHealthy=yes" .
//
// -X sets a string variable at link time. Production builds don't contain this
// variable at all (the linker ignores -X for a name that doesn't exist).
var devNeverHealthy string

func neverHealthy() bool { return devNeverHealthy == "yes" }
