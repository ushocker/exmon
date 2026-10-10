//go:build exmondev

// This file is only compiled with `go build -tags exmondev` (the //go:build line
// above is a build constraint). It adds the development public keys from
// trust-dev/, so a developer can sign test configs and releases with a local key
// file instead of Key Vault. Never ship an exmondev build to production PCs.

package signdoc

import (
	"embed"
	"io/fs"
)

//go:embed trust-dev
var devTrust embed.FS

func init() {
	sub, err := fs.Sub(devTrust, "trust-dev")
	if err == nil {
		extraTrust = append(extraTrust, sub)
	}
}
