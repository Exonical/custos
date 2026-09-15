// Package migrations embeds the SQL migration files.
package migrations

import "embed"

// FS contains the numbered up/down SQL migrations.
//
//go:embed *.sql
var FS embed.FS
