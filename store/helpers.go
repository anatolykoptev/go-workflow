package store

import (
	"embed"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// nilIfEmpty returns nil for an empty string, otherwise the string value.
// Used for nullable TEXT columns (e.g. idempotency_key).
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// runEmbeddedMigrations applies SQL migration files from an embedded FS.
// Files are read from the given subdirectory and executed sequentially by filename.
func runEmbeddedMigrations(db *sqlx.DB, migrations embed.FS, dir string) error {
	entries, err := migrations.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	for _, entry := range entries {
		sql, err := migrations.ReadFile(dir + "/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		if _, err := db.Exec(string(sql)); err != nil {
			return fmt.Errorf("exec %s: %w", entry.Name(), err)
		}
	}
	return nil
}
