package secrets

import (
	"encoding/base64"
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-07-1: Verifies that both the secrets and variables tables exist in the
// SQLite database with the correct columns, types, constraints, and composite
// primary key.
// Requirement: 07-REQ-1.1
// ---------------------------------------------------------------------------

func TestSecretsTableSchema_Columns(t *testing.T) {
	db := openTestDB(t)

	cols := queryTableInfo(t, db, "secrets")
	if len(cols) == 0 {
		t.Fatal("secrets table does not exist or has no columns")
	}

	expectedCols := []struct {
		name    string
		typ     string
		notNull int
	}{
		{"owner_type", "TEXT", 1},
		{"owner_id", "TEXT", 1},
		{"key", "TEXT", 1},
		{"value", "TEXT", 1},
		{"created_at", "TEXT", 1},
		{"updated_at", "TEXT", 1},
	}

	for _, exp := range expectedCols {
		col, found := findColumn(cols, exp.name)
		if !found {
			t.Errorf("secrets table missing column %q", exp.name)
			continue
		}
		if col.Type != exp.typ {
			t.Errorf("column %q type = %q; want %q", exp.name, col.Type, exp.typ)
		}
		if col.NotNull != exp.notNull {
			t.Errorf("column %q notnull = %d; want %d", exp.name, col.NotNull, exp.notNull)
		}
	}
}

func TestVariablesTableSchema_Columns(t *testing.T) {
	db := openTestDB(t)

	cols := queryTableInfo(t, db, "variables")
	if len(cols) == 0 {
		t.Fatal("variables table does not exist or has no columns")
	}

	// Variables table must have identical schema to secrets table.
	expectedCols := []struct {
		name    string
		typ     string
		notNull int
	}{
		{"owner_type", "TEXT", 1},
		{"owner_id", "TEXT", 1},
		{"key", "TEXT", 1},
		{"value", "TEXT", 1},
		{"created_at", "TEXT", 1},
		{"updated_at", "TEXT", 1},
	}

	for _, exp := range expectedCols {
		col, found := findColumn(cols, exp.name)
		if !found {
			t.Errorf("variables table missing column %q", exp.name)
			continue
		}
		if col.Type != exp.typ {
			t.Errorf("column %q type = %q; want %q", exp.name, col.Type, exp.typ)
		}
		if col.NotNull != exp.notNull {
			t.Errorf("column %q notnull = %d; want %d", exp.name, col.NotNull, exp.notNull)
		}
	}
}

func TestSecretsTableSchema_CompositePrimaryKey(t *testing.T) {
	db := openTestDB(t)

	cols := queryTableInfo(t, db, "secrets")
	if len(cols) == 0 {
		t.Fatal("secrets table does not exist or has no columns")
	}

	// Check that owner_type, owner_id, key form the composite PK.
	pkCols := make(map[string]int)
	for _, c := range cols {
		if c.PK > 0 {
			pkCols[c.Name] = c.PK
		}
	}

	if len(pkCols) != 3 {
		t.Fatalf("expected 3 PK columns; got %d: %v", len(pkCols), pkCols)
	}

	for _, name := range []string{"owner_type", "owner_id", "key"} {
		if _, ok := pkCols[name]; !ok {
			t.Errorf("column %q is not part of the primary key", name)
		}
	}
}

func TestVariablesTableSchema_CompositePrimaryKey(t *testing.T) {
	db := openTestDB(t)

	cols := queryTableInfo(t, db, "variables")
	if len(cols) == 0 {
		t.Fatal("variables table does not exist or has no columns")
	}

	pkCols := make(map[string]int)
	for _, c := range cols {
		if c.PK > 0 {
			pkCols[c.Name] = c.PK
		}
	}

	if len(pkCols) != 3 {
		t.Fatalf("expected 3 PK columns; got %d: %v", len(pkCols), pkCols)
	}

	for _, name := range []string{"owner_type", "owner_id", "key"} {
		if _, ok := pkCols[name]; !ok {
			t.Errorf("column %q is not part of the primary key", name)
		}
	}
}

func TestSecretsTableSchema_OwnerTypeCheckConstraint(t *testing.T) {
	db := openTestDB(t)

	// Valid owner types should be accepted.
	for _, validType := range []string{"user", "org", "workspace"} {
		seedSecret(t, db, validType, "test-id", "KEY_"+validType, "value")
	}

	// Invalid owner type should be rejected by the CHECK constraint.
	now := "2024-01-01T00:00:00Z"
	_, err := db.Exec(
		"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		"invalid_type", "test-id", "CHECK_KEY", "dmFsdWU=", now, now,
	)
	if err == nil {
		t.Error("INSERT with invalid owner_type succeeded; want CHECK constraint error")
	}
}

func TestSecretsTableSchema_NotNullConstraints(t *testing.T) {
	db := openTestDB(t)

	now := "2024-01-01T00:00:00Z"
	testCases := []struct {
		name  string
		query string
		args  []any
	}{
		{
			"null_owner_type",
			"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (NULL, ?, ?, ?, ?, ?)",
			[]any{"id", "KEY", "dmFsdWU=", now, now},
		},
		{
			"null_owner_id",
			"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, NULL, ?, ?, ?, ?)",
			[]any{"user", "KEY", "dmFsdWU=", now, now},
		},
		{
			"null_key",
			"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, NULL, ?, ?, ?)",
			[]any{"user", "id", "dmFsdWU=", now, now},
		},
		{
			"null_value",
			"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?)",
			[]any{"user", "id", "KEY", now, now},
		},
		{
			"null_created_at",
			"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, NULL, ?)",
			[]any{"user", "id", "KEY", "dmFsdWU=", now},
		},
		{
			"null_updated_at",
			"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, NULL)",
			[]any{"user", "id", "KEY", "dmFsdWU=", now},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(tc.query, tc.args...)
			if err == nil {
				t.Errorf("INSERT with %s succeeded; want NOT NULL constraint error", tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-07-2: Verifies that the store layer base64-encodes values before writing
// to the database and decodes them transparently on read.
// Requirement: 07-REQ-1.2
// ---------------------------------------------------------------------------

func TestStoreBase64Encoding_SecretsWrite(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	rawValue := "hello world\nline2"
	_, err := store.CreateSecrets("user", "test-user", []EntryInput{
		{Key: "MY_SECRET", Value: rawValue},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	// Verify the database stores the base64-encoded value, not raw.
	var dbValue string
	if err := db.QueryRow("SELECT value FROM secrets WHERE key = ?", "MY_SECRET").Scan(&dbValue); err != nil {
		t.Fatalf("query raw value: %v", err)
	}

	expectedBase64 := base64.StdEncoding.EncodeToString([]byte(rawValue))
	if dbValue != expectedBase64 {
		t.Errorf("stored value = %q; want base64 %q", dbValue, expectedBase64)
	}
}

func TestStoreBase64Encoding_VariablesReadDecode(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	rawValue := "hello world\nline2"
	_, err := store.CreateVariables("user", "test-user", []EntryInput{
		{Key: "MY_VAR", Value: rawValue},
	})
	if err != nil {
		t.Fatalf("CreateVariables() returned error: %v", err)
	}

	// List variables and verify the value is returned decoded (raw).
	vars, err := store.ListVariables("user", "test-user")
	if err != nil {
		t.Fatalf("ListVariables() returned error: %v", err)
	}
	if len(vars) != 1 {
		t.Fatalf("got %d variables; want 1", len(vars))
	}
	if vars[0].Value != rawValue {
		t.Errorf("value = %q; want %q (decoded)", vars[0].Value, rawValue)
	}
}

// ---------------------------------------------------------------------------
// TS-07-3: Verifies that no foreign key constraints exist on owner_id in the
// secrets and variables tables, and that cascading deletion is handled at the
// application layer.
// Requirement: 07-REQ-1.3
// ---------------------------------------------------------------------------

func TestNoForeignKeyConstraints_SecretsInsert(t *testing.T) {
	db := openTestDB(t)

	// Insert a row with a non-existent owner_id — should succeed because
	// there are no foreign key constraints on owner_id.
	now := "2024-01-01T00:00:00Z"
	encoded := base64.StdEncoding.EncodeToString([]byte("val"))
	_, err := db.Exec(
		"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		"user", "nonexistent-uuid", "FK_TEST_KEY", encoded, now, now,
	)
	if err != nil {
		t.Fatalf("INSERT with non-existent owner_id failed: %v", err)
	}
}

func TestNoForeignKeyConstraints_VariablesInsert(t *testing.T) {
	db := openTestDB(t)

	now := "2024-01-01T00:00:00Z"
	encoded := base64.StdEncoding.EncodeToString([]byte("val"))
	_, err := db.Exec(
		"INSERT INTO variables (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		"user", "nonexistent-uuid", "FK_TEST_KEY", encoded, now, now,
	)
	if err != nil {
		t.Fatalf("INSERT with non-existent owner_id failed: %v", err)
	}
}

func TestNoForeignKeyConstraints_PragmaCheck(t *testing.T) {
	db := openTestDB(t)

	// PRAGMA foreign_key_list should return no rows for both tables.
	for _, table := range []string{"secrets", "variables"} {
		rows, err := db.Query(fmt.Sprintf("PRAGMA foreign_key_list('%s')", table))
		if err != nil {
			t.Fatalf("PRAGMA foreign_key_list(%s) failed: %v", table, err)
		}
		hasFK := rows.Next()
		rows.Close()
		if hasFK {
			t.Errorf("table %s has foreign key constraints; want none", table)
		}
	}
}
