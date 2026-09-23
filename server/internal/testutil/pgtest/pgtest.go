// Package pgtest gives integration tests an isolated Postgres schema built from
// the project's init.sql.
package pgtest

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DSN creates a fresh schema with the project schema applied and returns a DSN
// whose connections use it. The schema is dropped when the test ends. Skips
// the test when POSTGRES_DSN is not set, unless REQUIRE_INTEGRATION is set:
// CI sets it so a missing DSN fails the build instead of passing silently.
func DSN(t *testing.T) string {
	t.Helper()
	base := os.Getenv("POSTGRES_DSN")
	if base == "" {
		if os.Getenv("REQUIRE_INTEGRATION") != "" {
			t.Fatal("REQUIRE_INTEGRATION is set but POSTGRES_DSN is empty")
		}
		t.Skip("integration test: POSTGRES_DSN not set")
	}

	ctx := context.Background()
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("connecting to postgres: %v", err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("creating schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("dropping schema: %v", err)
		}
	})

	dsn, err := withSearchPath(base, schema)
	if err != nil {
		t.Fatalf("building DSN: %v", err)
	}

	ddl, err := os.ReadFile(initSQLPath())
	if err != nil {
		t.Fatalf("reading init.sql: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to test schema: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, string(ddl)); err != nil {
		t.Fatalf("applying init.sql: %v", err)
	}
	return dsn
}

func withSearchPath(dsn, schema string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// initSQLPath locates init.sql at the repository root, relative to this file.
func initSQLPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "init.sql")
}
