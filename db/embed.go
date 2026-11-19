// Package db embeds the SQL migrations so the API binary can apply them
// without any files on disk.
package db

import "embed"

// Migrations holds every file in db/migrations.
//
//go:embed migrations/*.sql
var Migrations embed.FS
