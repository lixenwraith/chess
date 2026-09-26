// Package pgtest provisions isolated PostgreSQL schemas for tests.
//
// Set CHESS_TEST_DSN to a database the test role may create schemas in, e.g.
//
//	CHESS_TEST_DSN='postgres://chess_test:secret@127.0.0.1:5432/chess_test?sslmode=disable'
//
// Tests that need a database are skipped when it is unset.
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// EnvDSN names the environment variable holding the base test DSN.
const EnvDSN = "CHESS_TEST_DSN"

// DSN creates an empty schema, registers its removal as test cleanup, and
// returns the base DSN with search_path pinned to that schema.
func DSN(t testing.TB) string {
	t.Helper()
	base := os.Getenv(EnvDSN)
	if base == "" {
		t.Skipf("%s is not set; skipping PostgreSQL test", EnvDSN)
	}

	suffix := make([]byte, 6)
	rand.Read(suffix)
	schema := "chess_test_" + hex.EncodeToString(suffix)

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema %s: %v", schema, err)
		}
	})
	return WithParam(base, "search_path", schema)
}

// WithParam adds a runtime parameter to a URL or keyword/value DSN.
func WithParam(dsn, key, value string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err == nil {
			query := u.Query()
			query.Set(key, value)
			u.RawQuery = query.Encode()
			return u.String()
		}
	}
	return dsn + " " + key + "=" + value
}
