package jobqueue

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-1: InitSchema creates the jobs table and all four required indexes
// when called with a valid in-memory SQLite database.
// Requirement: 10-REQ-1.1
// ---------------------------------------------------------------------------

func TestInitSchema_CreatesTableAndIndexes(t *testing.T) {
	db := openTestDB(t)

	err := InitSchema(db)
	if err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	// Verify jobs table exists in sqlite_master.
	var tableName string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='jobs'").Scan(&tableName)
	if err != nil {
		t.Fatalf("jobs table not found in sqlite_master: %v", err)
	}

	// Verify all required columns exist with correct names.
	cols := queryTableInfo(t, db, "jobs")
	if len(cols) == 0 {
		t.Fatal("jobs table has no columns")
	}

	requiredColumns := []string{
		"id", "type", "key", "nonce", "status",
		"payload", "result", "error", "retry_count",
		"available_at", "submitted_by", "created_at", "updated_at",
	}
	for _, name := range requiredColumns {
		if _, found := findColumn(cols, name); !found {
			t.Errorf("jobs table missing required column %q", name)
		}
	}

	// Verify all four required indexes exist.
	requiredIndexes := []string{
		"idx_jobs_nonce",
		"idx_jobs_type_key_status",
		"idx_jobs_status_available_at",
		"idx_jobs_type_created_at",
	}
	for _, idx := range requiredIndexes {
		var indexName string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx,
		).Scan(&indexName)
		if err != nil {
			t.Errorf("required index %q not found in sqlite_master: %v", idx, err)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-10-2: InitSchema called a second time on a database where the jobs table
// already exists returns nil without error and leaves existing data intact.
// Requirement: 10-REQ-1.2
// Property: 10-PROP-10 (InitSchema is idempotent)
// ---------------------------------------------------------------------------

func TestInitSchema_Idempotent(t *testing.T) {
	db := openTestDB(t)

	// First call creates the schema.
	err := InitSchema(db)
	if err != nil {
		t.Fatalf("first InitSchema() returned error: %v", err)
	}

	// Insert a test row.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error,
		  retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES ('j1', 'merge', 'main', 'n1', 'queued', '{}', NULL, NULL, 0, ?, 'test', ?, ?)`,
		now, now, now,
	)
	if err != nil {
		t.Fatalf("insert into jobs failed: %v", err)
	}

	// Second call should succeed silently.
	err = InitSchema(db)
	if err != nil {
		t.Fatalf("second InitSchema() returned error: %v", err)
	}

	// Verify the previously inserted row is still present.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM jobs WHERE id='j1'").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row after second InitSchema(), got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-10-3: InitSchema creates idx_jobs_nonce as a UNIQUE index, causing a
// second insert with the same nonce to fail with a unique constraint violation.
// Requirement: 10-REQ-1.3
// ---------------------------------------------------------------------------

func TestInitSchema_NonceIndexIsUnique(t *testing.T) {
	db := openTestDB(t)

	err := InitSchema(db)
	if err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// First insert with nonce 'same-nonce' succeeds.
	_, err = db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error,
		  retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES ('j1', 'merge', 'k', 'same-nonce', 'queued', '{}', NULL, NULL, 0, ?, 'test', ?, ?)`,
		now, now, now,
	)
	if err != nil {
		t.Fatalf("first insert failed: %v", err)
	}

	// Second insert with the same nonce must fail.
	_, err = db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error,
		  retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES ('j2', 'merge', 'k', 'same-nonce', 'queued', '{}', NULL, NULL, 0, ?, 'test', ?, ?)`,
		now, now, now,
	)
	if err == nil {
		t.Fatal("expected error on duplicate nonce insert, got nil")
	}

	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "unique") {
		t.Errorf("expected unique constraint error, got: %v", err)
	}
}
