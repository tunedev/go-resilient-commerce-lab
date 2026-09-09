// Package migrations embeds the inventory service schema.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS returns the embedded migration files.
func FS() fs.FS {
	return files
}
