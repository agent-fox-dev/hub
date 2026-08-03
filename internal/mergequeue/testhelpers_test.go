package mergequeue

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database for test isolation.
// It calls InitSchema to create the merge_jobs table.
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

// openTestDBNoSchema opens an in-memory SQLite database without calling
// InitSchema. Useful for testing InitSchema itself.
func openTestDBNoSchema(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
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

// newTestUUID returns a deterministic UUID-like string for test use.
func newTestUUID(suffix string) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012s", suffix)
}

// insertTestMergeJob inserts a merge job row directly into the database
// using raw SQL, bypassing the store layer. Used for test setup.
func insertTestMergeJob(t *testing.T, db *sql.DB, id, nonce, workspaceSlug, targetBranch, sourceRef, status, submittedBy string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO merge_jobs (
		id, nonce, workspace_slug, target_branch, source_ref, status,
		retry_count, available_at, submitted_by, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		id, nonce, workspaceSlug, targetBranch, sourceRef, status,
		now, submittedBy, now, now,
	)
	if err != nil {
		t.Fatalf("insertTestMergeJob(%q, status=%q) failed: %v", id, status, err)
	}
}

// insertTestMergeJobFull inserts a merge job row with all fields specified.
func insertTestMergeJobFull(t *testing.T, db *sql.DB, job *MergeJob) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO merge_jobs (
		id, nonce, campaign_id, spec_id, workspace_slug, target_branch, source_ref,
		status, rejection_reason, retry_count, available_at, base_sha, merged_sha,
		conflict_details, check_output, submitted_by, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Nonce, job.CampaignID, job.SpecID,
		job.WorkspaceSlug, job.TargetBranch, job.SourceRef,
		job.Status, job.RejectionReason, job.RetryCount,
		job.AvailableAt, job.BaseSHA, job.MergedSHA,
		job.ConflictDetails, job.CheckOutput, job.SubmittedBy,
		job.CreatedAt, job.UpdatedAt,
	)
	if err != nil {
		t.Fatalf("insertTestMergeJobFull(%q, status=%q) failed: %v", job.ID, job.Status, err)
	}
}

// getJobStatus reads the current status of a merge job from the database.
func getJobStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var status string
	err := db.QueryRow("SELECT status FROM merge_jobs WHERE id = ?", id).Scan(&status)
	if err != nil {
		t.Fatalf("getJobStatus(%q) failed: %v", id, err)
	}
	return status
}

// getJobRowCount returns the number of rows in the merge_jobs table.
func getJobRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM merge_jobs").Scan(&count)
	if err != nil {
		t.Fatalf("getJobRowCount() failed: %v", err)
	}
	return count
}

// tableExists returns true if the named table exists in sqlite_master.
func tableExists(t *testing.T, db *sql.DB, tableName string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
		tableName,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("tableExists(%q) query failed: %v", tableName, err)
	}
	return true
}

// indexExists returns true if the named index exists in sqlite_master.
func indexExists(t *testing.T, db *sql.DB, indexName string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name=?",
		indexName,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("indexExists(%q) query failed: %v", indexName, err)
	}
	return true
}
