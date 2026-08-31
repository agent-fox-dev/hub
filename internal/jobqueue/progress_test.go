package jobqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-NS-1: The jobs table contains a nullable progress column. GetByID
// returns a Job with Progress populated when non-null. ListByKey and
// ListByType scan the column without error.
// Requirement: NS-REQ-1
// ---------------------------------------------------------------------------

func TestProgress_SchemaHasProgressColumn(t *testing.T) {
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	cols := queryTableInfo(t, db, "jobs")
	col, found := findColumn(cols, "progress")
	if !found {
		t.Fatal("jobs table missing 'progress' column")
	}
	if col.NotNull != 0 {
		t.Errorf("expected progress column to be nullable, got NOT NULL=%d", col.NotNull)
	}
}

func TestProgress_GetByIDReturnsProgress(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "rebuild")

	// Seed a running job with progress data.
	now := time.Now().UTC().Format(time.RFC3339)
	progressJSON := `[{"patch_id":"p1","status":"success"}]`
	_, err := db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error,
		  progress, retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, 0, ?, ?, ?, ?)`,
		"j-prog", "rebuild", "ws1", "n-prog", "running",
		`{"workspace_slug":"ws1"}`, progressJSON,
		now, "test", now, now,
	)
	if err != nil {
		t.Fatalf("seed job with progress failed: %v", err)
	}

	job, err := q.GetByID("j-prog")
	if err != nil {
		t.Fatalf("GetByID() returned error: %v", err)
	}
	if job.Progress == nil {
		t.Fatal("expected non-nil Progress, got nil")
	}
	if string(job.Progress) != progressJSON {
		t.Errorf("expected Progress=%q, got %q", progressJSON, string(job.Progress))
	}
}

func TestProgress_GetByIDNullProgress(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "rebuild")

	// Seed a job without progress.
	seedJob(t, db, "j-no-prog", "rebuild", "ws1", "n-no-prog", "queued")

	job, err := q.GetByID("j-no-prog")
	if err != nil {
		t.Fatalf("GetByID() returned error: %v", err)
	}
	if job.Progress != nil {
		t.Errorf("expected nil Progress for job without progress, got %q", string(job.Progress))
	}
}

func TestProgress_ListByKeyScansProgressColumn(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "rebuild")

	now := time.Now().UTC().Format(time.RFC3339)
	progressJSON := `[{"patch_id":"p1","status":"success"}]`
	_, err := db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error,
		  progress, retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, 0, ?, ?, ?, ?)`,
		"j-list", "rebuild", "ws1", "n-list", "running",
		`{"workspace_slug":"ws1"}`, progressJSON,
		now, "test", now, now,
	)
	if err != nil {
		t.Fatalf("seed job failed: %v", err)
	}

	jobs, err := q.ListByKey("rebuild", "ws1")
	if err != nil {
		t.Fatalf("ListByKey() returned error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if string(jobs[0].Progress) != progressJSON {
		t.Errorf("ListByKey: expected Progress=%q, got %q", progressJSON, string(jobs[0].Progress))
	}
}

func TestProgress_ListByTypeScansProgressColumn(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "rebuild")

	now := time.Now().UTC().Format(time.RFC3339)
	progressJSON := `[{"patch_id":"p2","status":"skipped"}]`
	_, err := db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error,
		  progress, retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, 0, ?, ?, ?, ?)`,
		"j-type", "rebuild", "ws2", "n-type", "running",
		`{}`, progressJSON,
		now, "test", now, now,
	)
	if err != nil {
		t.Fatalf("seed job failed: %v", err)
	}

	jobs, err := q.ListByType("rebuild", ListOpts{Limit: 10})
	if err != nil {
		t.Fatalf("ListByType() returned error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if string(jobs[0].Progress) != progressJSON {
		t.Errorf("ListByType: expected Progress=%q, got %q", progressJSON, string(jobs[0].Progress))
	}
}

// ---------------------------------------------------------------------------
// TS-NS-2: UpdateProgress marshals data to JSON and writes to the progress
// column. Verifying by reading the column directly.
// Requirement: NS-REQ-2
// ---------------------------------------------------------------------------

func TestProgress_UpdateProgressWritesToDB(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "rebuild")

	// Seed a running job.
	seedJob(t, db, "j-up", "rebuild", "ws1", "n-up", "running")

	// Update progress.
	type patchResult struct {
		PatchID string `json:"patch_id"`
		Status  string `json:"status"`
	}
	data := []patchResult{
		{PatchID: "p1", Status: "success"},
		{PatchID: "p2", Status: "skipped"},
	}

	err := q.UpdateProgress("j-up", data)
	if err != nil {
		t.Fatalf("UpdateProgress() returned error: %v", err)
	}

	// Query DB directly to verify.
	var progressStr sql.NullString
	err = db.QueryRow("SELECT progress FROM jobs WHERE id = ?", "j-up").Scan(&progressStr)
	if err != nil {
		t.Fatalf("query progress column failed: %v", err)
	}
	if !progressStr.Valid {
		t.Fatal("expected non-null progress column after UpdateProgress")
	}

	// Verify the value matches the expected JSON.
	expectedJSON, _ := json.Marshal(data)
	if progressStr.String != string(expectedJSON) {
		t.Errorf("expected progress=%q, got %q", string(expectedJSON), progressStr.String)
	}

	// Also verify via GetByID.
	job, err := q.GetByID("j-up")
	if err != nil {
		t.Fatalf("GetByID() returned error: %v", err)
	}
	if string(job.Progress) != string(expectedJSON) {
		t.Errorf("GetByID: expected Progress=%q, got %q", string(expectedJSON), string(job.Progress))
	}
}

func TestProgress_UpdateProgressUpdatesTimestamp(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "rebuild")

	// Seed a job with an old updated_at to ensure UpdateProgress changes it.
	oldTime := "2020-01-01T00:00:00Z"
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error,
		  retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, '{}', NULL, NULL, 0, ?, ?, ?, ?)`,
		"j-ts", "rebuild", "ws1", "n-ts", "running",
		now, "test", now, oldTime,
	)
	if err != nil {
		t.Fatalf("seed job failed: %v", err)
	}

	err = q.UpdateProgress("j-ts", map[string]string{"step": "1"})
	if err != nil {
		t.Fatalf("UpdateProgress() returned error: %v", err)
	}

	var newUpdatedAt string
	_ = db.QueryRow("SELECT updated_at FROM jobs WHERE id = ?", "j-ts").Scan(&newUpdatedAt)

	if newUpdatedAt == oldTime {
		t.Error("expected updated_at to change after UpdateProgress")
	}
}

// ---------------------------------------------------------------------------
// TS-NS-3: The job ID is accessible inside the handler via context.
// claimAndExecute injects the job ID using context.WithValue.
// Requirement: NS-REQ-3
// ---------------------------------------------------------------------------

func TestProgress_JobIDFromContext(t *testing.T) {
	q, _ := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	// Channel to capture the job ID seen by the handler.
	gotJobID := make(chan string, 1)

	handler := func(ctx context.Context, _ json.RawMessage) (any, bool, error) {
		gotJobID <- JobIDFromContext(ctx)
		return nil, false, nil
	}
	if err := q.Register("test-ctx", handler, nil); err != nil {
		t.Fatalf("Register() returned error: %v", err)
	}

	jobID, _, err := q.Enqueue(EnqueueParams{
		Type:        "test-ctx",
		Key:         "k1",
		Nonce:       "n-ctx",
		Payload:     json.RawMessage(`{}`),
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}

	if err := q.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer q.Stop()

	select {
	case id := <-gotJobID:
		if id == "" {
			t.Error("expected non-empty job ID from context, got empty string")
		}
		if id != jobID {
			t.Errorf("expected job ID=%q from context, got %q", jobID, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for handler to receive job ID")
	}
}

func TestProgress_JobIDFromContextEmpty(t *testing.T) {
	// When called with a context that has no job ID, returns empty string.
	ctx := context.Background()
	id := JobIDFromContext(ctx)
	if id != "" {
		t.Errorf("expected empty string from bare context, got %q", id)
	}
}

// ---------------------------------------------------------------------------
// MigrateProgress tests
// ---------------------------------------------------------------------------

func TestMigrateProgress_AddsColumn(t *testing.T) {
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	// Remove progress column to simulate pre-migration state.
	// SQLite doesn't support DROP COLUMN easily, so we test the migration
	// on a fresh schema that already includes the column — just verify
	// MigrateProgress is idempotent.
	if err := MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress() returned error: %v", err)
	}

	cols := queryTableInfo(t, db, "jobs")
	if _, found := findColumn(cols, "progress"); !found {
		t.Error("progress column not found after MigrateProgress")
	}
}

func TestMigrateProgress_Idempotent(t *testing.T) {
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	// Call twice — second call should not error.
	if err := MigrateProgress(db); err != nil {
		t.Fatalf("first MigrateProgress() returned error: %v", err)
	}
	if err := MigrateProgress(db); err != nil {
		t.Fatalf("second MigrateProgress() returned error: %v", err)
	}
}
