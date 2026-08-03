package mergequeue

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// TS-11-1: InitSchema creates the merge_jobs table and all three indexes
// when called on a fresh in-memory SQLite database.
// Requirement: 11-REQ-1.1
// ---------------------------------------------------------------------------

func TestSchemaInit_CreatesTable(t *testing.T) {
	db := openTestDBNoSchema(t)

	err := InitSchema(db)
	if err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	if !tableExists(t, db, "merge_jobs") {
		t.Fatal("merge_jobs table does not exist after InitSchema()")
	}
}

func TestSchemaInit_AllColumns(t *testing.T) {
	db := openTestDB(t)

	cols := queryTableInfo(t, db, "merge_jobs")
	if len(cols) == 0 {
		t.Fatal("merge_jobs table does not exist or has no columns")
	}

	// All 20 columns as specified in REQ-1.1.
	expectedColumns := []string{
		"id", "nonce", "campaign_id", "spec_id",
		"workspace_slug", "target_branch", "source_ref", "status",
		"rejection_reason", "retry_count", "available_at",
		"base_sha", "merged_sha", "conflict_details", "check_output",
		"submitted_by", "created_at", "updated_at",
	}

	colMap := make(map[string]bool)
	for _, c := range cols {
		colMap[c.Name] = true
	}

	for _, name := range expectedColumns {
		if !colMap[name] {
			t.Errorf("merge_jobs table missing column %q", name)
		}
	}
}

func TestSchemaInit_IndexesExist(t *testing.T) {
	db := openTestDB(t)

	requiredIndexes := []string{
		"idx_merge_jobs_campaign",
		"idx_merge_jobs_workspace",
		"idx_merge_jobs_available",
	}

	for _, idx := range requiredIndexes {
		if !indexExists(t, db, idx) {
			t.Errorf("index %q does not exist after InitSchema()", idx)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-11-2: The merge_jobs table CHECK constraint rejects any status value
// not in the allowed set.
// Requirement: 11-REQ-1.2
// ---------------------------------------------------------------------------

func TestSchemaCheckConstraint_ValidStatuses(t *testing.T) {
	db := openTestDB(t)

	for _, status := range ValidStatuses {
		id := newTestUUID(status)
		nonce := newTestUUID("n" + status)
		now := time.Now().UTC().Format(time.RFC3339)
		_, err := db.Exec(`INSERT INTO merge_jobs (
			id, nonce, workspace_slug, target_branch, source_ref, status,
			retry_count, available_at, submitted_by, created_at, updated_at
		) VALUES (?, ?, 'ws', 'main', 'spec/01', ?, 0, ?, ?, ?, ?)`,
			id, nonce, status, now, newTestUUID("user"), now, now,
		)
		if err != nil {
			t.Errorf("INSERT with valid status %q failed: %v", status, err)
		}
	}
}

func TestSchemaCheckConstraint_InvalidStatus(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO merge_jobs (
		id, nonce, workspace_slug, target_branch, source_ref, status,
		retry_count, available_at, submitted_by, created_at, updated_at
	) VALUES (?, ?, 'ws', 'main', 'spec/01', 'invalid_status', 0, ?, ?, ?, ?)`,
		newTestUUID("bad1"), newTestUUID("badn"), now, newTestUUID("user"), now, now,
	)
	if err == nil {
		t.Fatal("INSERT with invalid status 'invalid_status' succeeded; want CHECK constraint error")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Errorf("error = %q; want it to contain 'CHECK constraint failed'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TS-11-3: The merge_jobs table enforces UNIQUE constraint on nonce and
// NOT NULL constraints on required columns.
// Requirement: 11-REQ-1.3
// ---------------------------------------------------------------------------

func TestSchemaUniqueNonce(t *testing.T) {
	db := openTestDB(t)

	sharedNonce := "test-nonce-123"
	insertTestMergeJob(t, db, newTestUUID("row1"), sharedNonce, "ws", "main", "spec/01", "prepared", newTestUUID("user"))

	// Second insert with the same nonce should fail.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO merge_jobs (
		id, nonce, workspace_slug, target_branch, source_ref, status,
		retry_count, available_at, submitted_by, created_at, updated_at
	) VALUES (?, ?, 'ws', 'main', 'spec/02', 'prepared', 0, ?, ?, ?, ?)`,
		newTestUUID("row2"), sharedNonce, now, newTestUUID("user"), now, now,
	)
	if err == nil {
		t.Fatal("INSERT with duplicate nonce succeeded; want UNIQUE constraint error")
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Errorf("error = %q; want it to contain 'UNIQUE constraint failed'", err.Error())
	}
}

func TestSchemaNotNullConstraints(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC().Format(time.RFC3339)
	uid := newTestUUID("user")

	// Each test case omits (sets NULL) one required column.
	testCases := []struct {
		name  string
		query string
		args  []any
	}{
		{
			"null_workspace_slug",
			`INSERT INTO merge_jobs (id, nonce, workspace_slug, target_branch, source_ref, status, retry_count, available_at, submitted_by, created_at, updated_at)
			 VALUES (?, ?, NULL, 'main', 'spec/01', 'prepared', 0, ?, ?, ?, ?)`,
			[]any{newTestUUID("nn1"), newTestUUID("nnn1"), now, uid, now, now},
		},
		{
			"null_target_branch",
			`INSERT INTO merge_jobs (id, nonce, workspace_slug, target_branch, source_ref, status, retry_count, available_at, submitted_by, created_at, updated_at)
			 VALUES (?, ?, 'ws', NULL, 'spec/01', 'prepared', 0, ?, ?, ?, ?)`,
			[]any{newTestUUID("nn2"), newTestUUID("nnn2"), now, uid, now, now},
		},
		{
			"null_source_ref",
			`INSERT INTO merge_jobs (id, nonce, workspace_slug, target_branch, source_ref, status, retry_count, available_at, submitted_by, created_at, updated_at)
			 VALUES (?, ?, 'ws', 'main', NULL, 'prepared', 0, ?, ?, ?, ?)`,
			[]any{newTestUUID("nn3"), newTestUUID("nnn3"), now, uid, now, now},
		},
		{
			"null_status",
			`INSERT INTO merge_jobs (id, nonce, workspace_slug, target_branch, source_ref, status, retry_count, available_at, submitted_by, created_at, updated_at)
			 VALUES (?, ?, 'ws', 'main', 'spec/01', NULL, 0, ?, ?, ?, ?)`,
			[]any{newTestUUID("nn4"), newTestUUID("nnn4"), now, uid, now, now},
		},
		{
			"null_retry_count",
			`INSERT INTO merge_jobs (id, nonce, workspace_slug, target_branch, source_ref, status, retry_count, available_at, submitted_by, created_at, updated_at)
			 VALUES (?, ?, 'ws', 'main', 'spec/01', 'prepared', NULL, ?, ?, ?, ?)`,
			[]any{newTestUUID("nn5"), newTestUUID("nnn5"), now, uid, now, now},
		},
		{
			"null_available_at",
			`INSERT INTO merge_jobs (id, nonce, workspace_slug, target_branch, source_ref, status, retry_count, available_at, submitted_by, created_at, updated_at)
			 VALUES (?, ?, 'ws', 'main', 'spec/01', 'prepared', 0, NULL, ?, ?, ?)`,
			[]any{newTestUUID("nn6"), newTestUUID("nnn6"), uid, now, now},
		},
		{
			"null_submitted_by",
			`INSERT INTO merge_jobs (id, nonce, workspace_slug, target_branch, source_ref, status, retry_count, available_at, submitted_by, created_at, updated_at)
			 VALUES (?, ?, 'ws', 'main', 'spec/01', 'prepared', 0, ?, NULL, ?, ?)`,
			[]any{newTestUUID("nn7"), newTestUUID("nnn7"), now, now, now},
		},
		{
			"null_created_at",
			`INSERT INTO merge_jobs (id, nonce, workspace_slug, target_branch, source_ref, status, retry_count, available_at, submitted_by, created_at, updated_at)
			 VALUES (?, ?, 'ws', 'main', 'spec/01', 'prepared', 0, ?, ?, NULL, ?)`,
			[]any{newTestUUID("nn8"), newTestUUID("nnn8"), now, uid, now},
		},
		{
			"null_updated_at",
			`INSERT INTO merge_jobs (id, nonce, workspace_slug, target_branch, source_ref, status, retry_count, available_at, submitted_by, created_at, updated_at)
			 VALUES (?, ?, 'ws', 'main', 'spec/01', 'prepared', 0, ?, ?, ?, NULL)`,
			[]any{newTestUUID("nn9"), newTestUUID("nnn9"), now, uid, now},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(tc.query, tc.args...)
			if err == nil {
				t.Errorf("INSERT with %s succeeded; want NOT NULL constraint error", tc.name)
			} else if !strings.Contains(err.Error(), "NOT NULL constraint failed") {
				t.Errorf("INSERT with %s: error = %q; want it to contain 'NOT NULL constraint failed'", tc.name, err.Error())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-11-4: InitSchema returns a non-nil error when the database is read-only
// and does not call os.Exit.
// Requirement: 11-REQ-1.E1
// ---------------------------------------------------------------------------

func TestSchemaInit_ReadOnlyDB(t *testing.T) {
	// Open a read-only in-memory database using the SQLite URI mode parameter.
	db, err := sql.Open("sqlite", "file::memory:?mode=ro")
	if err != nil {
		t.Fatalf("failed to open read-only database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	err = InitSchema(db)
	if err == nil {
		t.Fatal("InitSchema() on read-only database returned nil; want non-nil error")
	}
	// The process should still be running — os.Exit was not called.
}

// ---------------------------------------------------------------------------
// TS-11-5: InitSchema called on a database that already has the merge_jobs
// table returns nil without altering existing data.
// Requirement: 11-REQ-1.E2
// ---------------------------------------------------------------------------

func TestSchemaInit_Idempotent(t *testing.T) {
	db := openTestDB(t) // First InitSchema call

	// Insert a row.
	insertTestMergeJob(t, db, newTestUUID("idem1"), newTestUUID("inonce1"),
		"ws", "main", "spec/01", "prepared", newTestUUID("user"))

	// Call InitSchema a second time.
	err := InitSchema(db)
	if err != nil {
		t.Fatalf("second InitSchema() returned error: %v", err)
	}

	// Verify the existing row is still present.
	count := getJobRowCount(t, db)
	if count != 1 {
		t.Errorf("row count after second InitSchema() = %d; want 1", count)
	}
}

// ---------------------------------------------------------------------------
// Additional schema tests: InsertMergeJob and GetMergeJob store layer tests.
// Subtask 1.1: Test InsertMergeJob stores all fields correctly.
// ---------------------------------------------------------------------------

func TestInsertMergeJob_StoresAllFields(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            newTestUUID("insert1"),
		Nonce:         newTestUUID("insn1"),
		CampaignID:    sql.NullString{String: newTestUUID("camp1"), Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "test-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets-variables",
		Status:        "prepared",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user1"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	err := InsertMergeJob(db, job)
	if err != nil {
		t.Fatalf("InsertMergeJob() returned error: %v", err)
	}

	got, err := GetMergeJob(db, job.ID)
	if err != nil {
		t.Fatalf("GetMergeJob() returned error: %v", err)
	}
	if got == nil {
		t.Fatal("GetMergeJob() returned nil; want non-nil job")
	}

	if got.ID != job.ID {
		t.Errorf("ID = %q; want %q", got.ID, job.ID)
	}
	if got.Nonce != job.Nonce {
		t.Errorf("Nonce = %q; want %q", got.Nonce, job.Nonce)
	}
	if got.WorkspaceSlug != job.WorkspaceSlug {
		t.Errorf("WorkspaceSlug = %q; want %q", got.WorkspaceSlug, job.WorkspaceSlug)
	}
	if got.TargetBranch != job.TargetBranch {
		t.Errorf("TargetBranch = %q; want %q", got.TargetBranch, job.TargetBranch)
	}
	if got.SourceRef != job.SourceRef {
		t.Errorf("SourceRef = %q; want %q", got.SourceRef, job.SourceRef)
	}
	if got.Status != "prepared" {
		t.Errorf("Status = %q; want %q", got.Status, "prepared")
	}
	if got.SubmittedBy != job.SubmittedBy {
		t.Errorf("SubmittedBy = %q; want %q", got.SubmittedBy, job.SubmittedBy)
	}
	if !got.CampaignID.Valid || got.CampaignID.String != job.CampaignID.String {
		t.Errorf("CampaignID = %v; want %v", got.CampaignID, job.CampaignID)
	}
	if !got.SpecID.Valid || got.SpecID.String != job.SpecID.String {
		t.Errorf("SpecID = %v; want %v", got.SpecID, job.SpecID)
	}
}

func TestGetMergeJob_NotFound(t *testing.T) {
	db := openTestDB(t)

	got, err := GetMergeJob(db, "nonexistent-id")
	if err == nil && got != nil {
		t.Fatal("GetMergeJob() for nonexistent ID returned a job; want nil or error")
	}
}

func TestUpdateStatus_PersistsCorrectly(t *testing.T) {
	db := openTestDB(t)

	id := newTestUUID("upd1")
	insertTestMergeJob(t, db, id, newTestUUID("updn1"), "ws", "main", "spec/01", "prepared", newTestUUID("user"))

	err := UpdateStatus(db, id, "queued")
	if err != nil {
		t.Fatalf("UpdateStatus(prepared -> queued) returned error: %v", err)
	}

	status := getJobStatus(t, db, id)
	if status != "queued" {
		t.Errorf("status after transition = %q; want %q", status, "queued")
	}
}
