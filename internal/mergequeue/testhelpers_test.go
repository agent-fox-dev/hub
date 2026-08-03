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

// openCanMergeTestDB opens an in-memory SQLite database with the merge_jobs,
// campaigns, and campaign_specs tables created directly via SQL. This avoids
// depending on InitSchema (which may still be a stub) while providing all
// tables that CanMerge queries.
func openCanMergeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDBNoSchema(t)
	setupMergeJobsTable(t, db)
	setupCampaignTables(t, db)
	return db
}

// setupMergeJobsTable creates the merge_jobs table directly via SQL,
// bypassing InitSchema. Used by CanMerge tests that need the table to exist
// before InitSchema is implemented.
func setupMergeJobsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS merge_jobs (
			id               TEXT PRIMARY KEY,
			nonce            TEXT NOT NULL UNIQUE,
			campaign_id      TEXT,
			spec_id          TEXT,
			workspace_slug   TEXT NOT NULL,
			target_branch    TEXT NOT NULL,
			source_ref       TEXT NOT NULL,
			status           TEXT NOT NULL
				CHECK(status IN ('prepared','queued','running','merged',
					'conflict','check_failed','cancelled','push_failed','dead_letter')),
			rejection_reason TEXT,
			retry_count      INTEGER NOT NULL,
			available_at     TEXT NOT NULL,
			base_sha         TEXT,
			merged_sha       TEXT,
			conflict_details TEXT,
			check_output     TEXT,
			submitted_by     TEXT NOT NULL,
			created_at       TEXT NOT NULL,
			updated_at       TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_merge_jobs_campaign
			ON merge_jobs(campaign_id, status);
		CREATE INDEX IF NOT EXISTS idx_merge_jobs_workspace
			ON merge_jobs(workspace_slug, status, created_at);
		CREATE INDEX IF NOT EXISTS idx_merge_jobs_available
			ON merge_jobs(status, available_at);
	`)
	if err != nil {
		t.Fatalf("setupMergeJobsTable() failed: %v", err)
	}
}

// setupCampaignTables creates the campaigns and campaign_specs tables in the
// test database. These tables are owned by the campaign package but queried
// by CanMerge for dependency and spec-status checks.
func setupCampaignTables(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS campaigns (
			id                 TEXT PRIMARY KEY,
			workspace_slug     TEXT NOT NULL,
			name               TEXT NOT NULL,
			integration_branch TEXT NOT NULL,
			status             TEXT NOT NULL DEFAULT 'active',
			dag                TEXT NOT NULL,
			created_by         TEXT NOT NULL,
			created_at         TEXT NOT NULL,
			updated_at         TEXT NOT NULL,
			UNIQUE(workspace_slug, name)
		);
		CREATE TABLE IF NOT EXISTS campaign_specs (
			campaign_id      TEXT NOT NULL REFERENCES campaigns(id),
			spec_id          TEXT NOT NULL,
			status           TEXT NOT NULL DEFAULT 'pending',
			branch_name      TEXT,
			branch_sha       TEXT,
			conflict_details TEXT,
			blocked_by_merge TEXT,
			updated_at       TEXT NOT NULL,
			PRIMARY KEY(campaign_id, spec_id)
		);
	`)
	if err != nil {
		t.Fatalf("setupCampaignTables() failed: %v", err)
	}
}

// insertTestCampaign inserts a campaign row into the test database.
func insertTestCampaign(t *testing.T, db *sql.DB, id, workspaceSlug, integrationBranch, dagJSON, now string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO campaigns (
		id, workspace_slug, name, integration_branch, status, dag,
		created_by, created_at, updated_at
	) VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?)`,
		id, workspaceSlug, "campaign-"+id[:8], integrationBranch,
		dagJSON, newTestUUID("creator"), now, now,
	)
	if err != nil {
		t.Fatalf("insertTestCampaign(%q) failed: %v", id, err)
	}
}

// insertTestCampaignSpec inserts a campaign_spec row into the test database.
// branchSHA may be empty to represent a NULL branch_sha (branch not ready).
func insertTestCampaignSpec(t *testing.T, db *sql.DB, campaignID, specID, status, branchSHA, now string) {
	t.Helper()
	var sha *string
	if branchSHA != "" {
		sha = &branchSHA
	}
	_, err := db.Exec(`INSERT INTO campaign_specs (
		campaign_id, spec_id, status, branch_name, branch_sha, updated_at
	) VALUES (?, ?, ?, ?, ?, ?)`,
		campaignID, specID, status, "spec/"+specID, sha, now,
	)
	if err != nil {
		t.Fatalf("insertTestCampaignSpec(%q, spec=%q, status=%q) failed: %v",
			campaignID, specID, status, err)
	}
}
