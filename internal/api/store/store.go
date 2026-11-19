// Package store owns the Postgres connection pool and the schema migrations.
// The sqlc-generated query code lives here too (added in milestone 1).
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"zynthera/db"
)

// Open creates a connection pool and checks that the database answers.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	return pool, nil
}

// migrateURL turns a postgres:// URL into the pgx5:// form that
// golang-migrate's pgx v5 driver expects.
func migrateURL(databaseURL string) (string, error) {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if rest, ok := strings.CutPrefix(databaseURL, prefix); ok {
			return "pgx5://" + rest, nil
		}
	}
	return "", errors.New("database URL must start with postgres:// or postgresql://")
}

func newMigrator(databaseURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(db.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("load embedded migrations: %w", err)
	}
	target, err := migrateURL(databaseURL)
	if err != nil {
		return nil, err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, target)
	if err != nil {
		return nil, fmt.Errorf("open migrator: %w", err)
	}
	return m, nil
}

func closeMigrator(m *migrate.Migrate) error {
	srcErr, dbErr := m.Close()
	return errors.Join(srcErr, dbErr)
}

// MigrateUp applies every pending migration. It is safe to run repeatedly.
func MigrateUp(databaseURL string) (err error) {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeMigrator(m)) }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// MigrateDown rolls back the given number of migrations.
func MigrateDown(databaseURL string, steps int) (err error) {
	if steps <= 0 {
		return errors.New("steps must be positive")
	}
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeMigrator(m)) }()
	if err := m.Steps(-steps); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("roll back migrations: %w", err)
	}
	return nil
}

// MigrateVersion reports the current schema version. Version 0 means no
// migration has been applied yet.
func MigrateVersion(databaseURL string) (version uint, dirty bool, err error) {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return 0, false, err
	}
	defer func() { err = errors.Join(err, closeMigrator(m)) }()
	version, dirty, err = m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return version, dirty, err
}
