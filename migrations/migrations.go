// Package migrations embeds the SQL migrations applied by cmd/migrate.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
