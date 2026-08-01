package postgres

import (
	"context"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate brings the schema up to date.
//
// The files are embedded so nothing has to be on the PATH to run them, and the
// integration tests apply this same FS to their throwaway database — so the
// schema the tests run against cannot drift from the one the service does.
func Migrate(ctx context.Context, db *DB) error {
	goose.SetBaseFS(migrations)
	goose.SetLogger(goose.NopLogger())

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db.pool, "migrations"); err != nil {
		return fmt.Errorf("migrating: %w", err)
	}
	return nil
}
