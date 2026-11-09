// Package migrations carries SQL migrations with the server binary.
package migrations

import "embed"

// Files is the packaged migration source, independent of the working directory.
//
//go:embed *.sql
var Files embed.FS
