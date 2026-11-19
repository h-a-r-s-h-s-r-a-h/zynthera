// Package testdb connects integration tests to the zynthera_test database and
// a separate Redis database. Both come from Docker Compose (`make up`).
//
// Packages that use it share one database, so run them one at a time:
// `go test -p 1 ./...` (this is what `make test` does).
package testdb

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"zynthera/internal/api/store"
	"zynthera/internal/shared/config"
)

var (
	once       sync.Once
	dbURL      string
	redisURL   string
	configured bool
	migrateErr error
)

// findRepoRoot walks up from the working directory to the folder with go.mod.
func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "PROJECT_SPEC.md")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// resolve works out the test URLs: explicit ZYN_TEST_* variables first,
// otherwise derived from the developer's .env.
func resolve() {
	dbURL = os.Getenv("ZYN_TEST_DATABASE_URL")
	redisURL = os.Getenv("ZYN_TEST_REDIS_URL")
	if dbURL != "" && redisURL != "" {
		configured = true
		return
	}
	root := findRepoRoot()
	if root == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(root, ".env")); err != nil {
		return
	}
	if err := config.LoadDotEnv(filepath.Join(root, ".env")); err != nil {
		return
	}
	if dbURL == "" {
		u, err := url.Parse(os.Getenv("ZYN_DATABASE_URL"))
		if err != nil || u.Path == "" {
			return
		}
		u.Path += "_test"
		dbURL = u.String()
	}
	if redisURL == "" {
		u, err := url.Parse(os.Getenv("ZYN_REDIS_URL"))
		if err != nil {
			return
		}
		// Database 1 is for tests; development uses database 0.
		u.Path = "/1"
		redisURL = u.String()
	}
	configured = true
}

// Open returns a pool on the migrated, emptied test database and a client on
// the emptied test Redis database. Without any configuration (no ZYN_TEST_*
// variables and no .env) the test is skipped.
func Open(t *testing.T) (*pgxpool.Pool, *redis.Client) {
	t.Helper()
	once.Do(func() {
		resolve()
		if configured {
			migrateErr = store.MigrateUp(dbURL)
		}
	})
	if !configured {
		t.Skip("no test database configured: run `make up` with a .env, or set ZYN_TEST_DATABASE_URL and ZYN_TEST_REDIS_URL")
	}
	if !strings.Contains(dbURL, "_test") {
		t.Fatalf("refusing to run tests against a database whose name lacks _test")
	}
	if migrateErr != nil {
		t.Fatalf("migrate test database (is `make up` running?): %v", migrateErr)
	}

	ctx := context.Background()
	pool, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatalf("open test database (is `make up` running?): %v", err)
	}
	t.Cleanup(pool.Close)

	// Empty every table except the migration bookkeeping.
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, `"`+name+`"`)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(tables) > 0 {
		if _, err := pool.Exec(ctx, "TRUNCATE "+strings.Join(tables, ", ")+" RESTART IDENTITY CASCADE"); err != nil {
			t.Fatal(err)
		}
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	if opts.DB == 0 {
		t.Fatalf("refusing to flush Redis database 0; tests must use another database")
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush test redis (is `make up` running?): %v", err)
	}
	return pool, rdb
}
