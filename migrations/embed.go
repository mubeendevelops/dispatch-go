// Package migrations embeds the SQL migration files into the compiled binary via
// go:embed. The application can then run migrations from a single self-contained
// artifact -- no need to ship the .sql files alongside the binary at deploy time.
package migrations

import "embed"

// Files holds every .sql migration in this directory. The store's migration
// runner reads the *.up.sql files from here.
//
//go:embed *.sql
var Files embed.FS
