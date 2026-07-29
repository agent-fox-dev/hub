package secrets

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database for test isolation.
// It calls InitSchema to create the secrets and variables tables.
// SQLite in-memory databases are per-connection, so the pool is capped
// at one connection to ensure all operations share the same database.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	return db
}

// columnInfo holds metadata for a single table column from PRAGMA table_info.
type columnInfo struct {
	Name    string
	Type    string
	NotNull int
	PK      int
}

// queryTableInfo returns column metadata for the given table using PRAGMA table_info.
func queryTableInfo(t *testing.T, db *sql.DB, tableName string) []columnInfo {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info('%s')", tableName))
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s) failed: %v", tableName, err)
	}
	defer rows.Close()

	var cols []columnInfo
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dfltValue *string
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan column info: %v", err)
		}
		cols = append(cols, columnInfo{Name: name, Type: typ, NotNull: notnull, PK: pk})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}
	return cols
}

// findColumn searches for a column by name in the column list.
func findColumn(cols []columnInfo, name string) (columnInfo, bool) {
	for _, c := range cols {
		if c.Name == name {
			return c, true
		}
	}
	return columnInfo{}, false
}

// seedSecret inserts a secret row directly into the database for test setup.
// The value is base64-encoded before insertion, mirroring the store's behavior.
func seedSecret(t *testing.T, db *sql.DB, ownerType, ownerID, key, rawValue string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	encoded := base64.StdEncoding.EncodeToString([]byte(rawValue))
	_, err := db.Exec(
		"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		ownerType, ownerID, key, encoded, now, now,
	)
	if err != nil {
		t.Fatalf("seedSecret(%q, %q, %q) returned error: %v", ownerType, ownerID, key, err)
	}
}

// seedSecrets inserts count secrets with sequential keys for the given scope.
func seedSecrets(t *testing.T, db *sql.DB, ownerType, ownerID string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("KEY_%d", i)
		seedSecret(t, db, ownerType, ownerID, key, fmt.Sprintf("value_%d", i))
	}
}

// seedVariables inserts count variables with sequential keys for the given scope.
func seedVariables(t *testing.T, db *sql.DB, ownerType, ownerID string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("VAR_%d", i)
		seedVariable(t, db, ownerType, ownerID, key, fmt.Sprintf("value_%d", i))
	}
}

// seedVariable inserts a variable row directly into the database for test setup.
// The value is base64-encoded before insertion, mirroring the store's behavior.
func seedVariable(t *testing.T, db *sql.DB, ownerType, ownerID, key, rawValue string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	encoded := base64.StdEncoding.EncodeToString([]byte(rawValue))
	_, err := db.Exec(
		"INSERT INTO variables (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		ownerType, ownerID, key, encoded, now, now,
	)
	if err != nil {
		t.Fatalf("seedVariable(%q, %q, %q) returned error: %v", ownerType, ownerID, key, err)
	}
}
