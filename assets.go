// Package homelab exposes the production frontend as an embedded filesystem.
package homelab

import (
	"embed"
	"io/fs"
)

// StaticFS is populated at compile time; the runtime image needs only the Go
// binary and CA certificates.
//
//go:embed static
var staticFS embed.FS

func Static() (fs.FS, error) {
	return fs.Sub(staticFS, "static")
}
