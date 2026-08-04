package jobqueue

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-33: GetByID with a valid job UUID returns the full job record
// including payload and result fields.
// Requirement: 10-REQ-11.1
// ---------------------------------------------------------------------------

func TestQuery_GetByID(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed a completed job with payload and result directly.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error, retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, NULL, 0, ?, ?, ?, ?)`,
		"j1", "merge", "main", "n1", "completed",
		`{"branch":"main"}`, `{"sha":"abc"}`,
		now, "user-1", now, now,
	)
	if err != nil {
		t.Fatalf("seed job failed: %v", err)
	}

	job, err := q.GetByID("j1")
	if err != nil {
		t.Fatalf("GetByID() returned error: %v", err)
	}
	if job == nil {
		t.Fatal("GetByID() returned nil job")
	}

	if job.ID != "j1" {
		t.Errorf("expected ID='j1', got %q", job.ID)
	}
	if job.Type != "merge" {
		t.Errorf("expected Type='merge', got %q", job.Type)
	}
	if job.Key != "main" {
		t.Errorf("expected Key='main', got %q", job.Key)
	}
	if job.Status != "completed" {
		t.Errorf("expected Status='completed', got %q", job.Status)
	}
	if string(job.Payload) != `{"branch":"main"}` {
		t.Errorf("expected Payload=%q, got %q", `{"branch":"main"}`, string(job.Payload))
	}
	if string(job.Result) != `{"sha":"abc"}` {
		t.Errorf("expected Result=%q, got %q", `{"sha":"abc"}`, string(job.Result))
	}
}

// ---------------------------------------------------------------------------
// TS-10-E33: GetByID with a UUID that does not exist returns (nil, non-nil
// error) with a not-found message.
// Requirement: 10-REQ-11.E1
// ---------------------------------------------------------------------------

func TestQuery_GetByIDNotFound(t *testing.T) {
	q, _ := newTestQueue(t)

	job, err := q.GetByID("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for non-existent job ID, got nil")
	}
	if job != nil {
		t.Errorf("expected nil job, got %+v", job)
	}
}

// ---------------------------------------------------------------------------
// TS-10-34: ListByType returns jobs of the given type filtered by optional
// status and key, ordered by created_at descending, with pagination applied.
// Requirement: 10-REQ-11.2
// ---------------------------------------------------------------------------

func TestQuery_ListByType(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed three jobs with different statuses and created_at times.
	t1 := time.Now().Add(-3 * time.Hour)
	t2 := time.Now().Add(-2 * time.Hour)
	t3 := time.Now().Add(-1 * time.Hour)

	seedJobFull(t, db, "j1", "merge", "main", "n1", "queued", 0, t1, t1)
	seedJobFull(t, db, "j2", "merge", "dev", "n2", "completed", 0, t2, t2)
	seedJobFull(t, db, "j3", "merge", "main", "n3", "completed", 0, t3, t3)

	// Filter by status=completed: should return j3, j2 (descending created_at).
	jobs, err := q.ListByType("merge", ListOpts{
		Status: "completed",
		Limit:  10,
		Offset: 0,
	})
	if err != nil {
		t.Fatalf("ListByType() returned error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
	if jobs[0].ID != "j3" {
		t.Errorf("expected first job ID='j3', got %q", jobs[0].ID)
	}
	if jobs[1].ID != "j2" {
		t.Errorf("expected second job ID='j2', got %q", jobs[1].ID)
	}
}

// ---------------------------------------------------------------------------
// TS-10-34 (cont.): ListByType filters by key when provided.
// Requirement: 10-REQ-11.2
// ---------------------------------------------------------------------------

func TestQuery_ListByTypeWithKeyFilter(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	t1 := time.Now().Add(-3 * time.Hour)
	t2 := time.Now().Add(-2 * time.Hour)

	seedJobFull(t, db, "j1", "merge", "main", "n1", "queued", 0, t1, t1)
	seedJobFull(t, db, "j2", "merge", "dev", "n2", "queued", 0, t2, t2)

	// Filter by key=main: should return only j1.
	jobs, err := q.ListByType("merge", ListOpts{
		Key:   "main",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListByType() returned error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job with key='main', got %d", len(jobs))
	}
	if jobs[0].ID != "j1" {
		t.Errorf("expected job ID='j1', got %q", jobs[0].ID)
	}
}

// ---------------------------------------------------------------------------
// TS-10-34 (cont.): ListByType with pagination (offset/limit).
// Requirement: 10-REQ-11.2
// ---------------------------------------------------------------------------

func TestQuery_ListByTypePagination(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed 5 jobs.
	for i := 0; i < 5; i++ {
		created := time.Now().Add(time.Duration(-5+i) * time.Hour)
		seedJobFull(t, db, "j"+string(rune('1'+i)), "merge", "main",
			"n"+string(rune('1'+i)), "queued", 0, created, created)
	}

	// Request page with offset=1, limit=2.
	jobs, err := q.ListByType("merge", ListOpts{
		Offset: 1,
		Limit:  2,
	})
	if err != nil {
		t.Fatalf("ListByType() returned error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs with offset=1 limit=2, got %d", len(jobs))
	}
}

// ---------------------------------------------------------------------------
// TS-10-34 (cont.): ListByType returns empty slice (not nil) when no jobs
// match.
// Requirement: 10-REQ-11.2
// ---------------------------------------------------------------------------

func TestQuery_ListByTypeNoMatch(t *testing.T) {
	q, _ := newTestQueue(t)

	jobs, err := q.ListByType("nonexistent", ListOpts{Limit: 10})
	if err != nil {
		t.Fatalf("ListByType() returned error: %v", err)
	}
	if jobs == nil {
		t.Fatal("expected empty slice (not nil) when no jobs match")
	}
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(jobs))
	}
}

// ---------------------------------------------------------------------------
// TS-10-E34: ListByType with limit=0 applies a default limit or returns an
// error; does not return an unbounded result set.
// Requirement: 10-REQ-11.E2
// ---------------------------------------------------------------------------

func TestQuery_ListByTypeLimitZero(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed 60 jobs to exceed a reasonable default limit.
	for i := 0; i < 60; i++ {
		created := time.Now().Add(time.Duration(-60+i) * time.Minute)
		id := "j" + time.Now().Format("150405") + string(rune('A'+i%26)) + string(rune('a'+i/26))
		nonce := "n" + id
		seedJobFull(t, db, id, "merge", "main", nonce, "queued", 0, created, created)
	}

	jobs, err := q.ListByType("merge", ListOpts{Limit: 0})
	if err != nil {
		// Some implementations reject limit=0 with an error. That's acceptable.
		return
	}

	// If it returns successfully, the result should be bounded by a default limit.
	if len(jobs) > 50 {
		t.Errorf("expected at most default limit (e.g. 50) jobs with Limit=0, got %d", len(jobs))
	}
}

// ---------------------------------------------------------------------------
// TS-10-35: ListByKey returns all job records for a specific (type, key)
// combination ordered by created_at descending.
// Requirement: 10-REQ-11.3
// ---------------------------------------------------------------------------

func TestQuery_ListByKey(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	t1 := time.Now().Add(-3 * time.Hour)
	t2 := time.Now().Add(-2 * time.Hour)

	seedJobFull(t, db, "j1", "merge", "main", "n1", "queued", 0, t1, t1)
	seedJobFull(t, db, "j2", "merge", "main", "n2", "completed", 0, t2, t2)
	seedJobFull(t, db, "j3", "merge", "dev", "n3", "queued", 0, t2, t2)

	jobs, err := q.ListByKey("merge", "main")
	if err != nil {
		t.Fatalf("ListByKey() returned error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs for (merge, main), got %d", len(jobs))
	}

	// Should be ordered by created_at descending: j2 (newer) before j1.
	if jobs[0].ID != "j2" {
		t.Errorf("expected first job ID='j2', got %q", jobs[0].ID)
	}
	if jobs[1].ID != "j1" {
		t.Errorf("expected second job ID='j1', got %q", jobs[1].ID)
	}
}

// ---------------------------------------------------------------------------
// TS-10-35 (cont.): ListByKey returns empty slice when no jobs match.
// Requirement: 10-REQ-11.3
// ---------------------------------------------------------------------------

func TestQuery_ListByKeyNoMatch(t *testing.T) {
	q, _ := newTestQueue(t)

	jobs, err := q.ListByKey("merge", "nonexistent")
	if err != nil {
		t.Fatalf("ListByKey() returned error: %v", err)
	}
	if jobs == nil {
		t.Fatal("expected empty slice (not nil) when no jobs match")
	}
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(jobs))
	}
}

// ---------------------------------------------------------------------------
// TS-10-36: CountByStatus returns a map of status to count for all jobs of
// the given type, omitting statuses with zero jobs.
// Requirement: 10-REQ-11.4
// ---------------------------------------------------------------------------

func TestQuery_CountByStatus(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed: 2 queued, 1 completed, 0 failed.
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "main", "n1", "queued", 0, now, now)
	seedJobFull(t, db, "j2", "merge", "dev", "n2", "queued", 0, now, now)
	seedJobFull(t, db, "j3", "merge", "feat", "n3", "completed", 0, now, now)

	counts, err := q.CountByStatus("merge")
	if err != nil {
		t.Fatalf("CountByStatus() returned error: %v", err)
	}
	if counts == nil {
		t.Fatal("expected non-nil map from CountByStatus")
	}

	if counts["queued"] != 2 {
		t.Errorf("expected counts['queued']=2, got %d", counts["queued"])
	}
	if counts["completed"] != 1 {
		t.Errorf("expected counts['completed']=1, got %d", counts["completed"])
	}
	if _, ok := counts["failed"]; ok {
		t.Error("expected 'failed' key to be absent from map (zero count)")
	}
}

// ---------------------------------------------------------------------------
// TS-10-E36: CountByStatus for a type with no jobs returns an empty map.
// Requirement: 10-REQ-11.E4
// ---------------------------------------------------------------------------

func TestQuery_CountByStatusEmpty(t *testing.T) {
	q, _ := newTestQueue(t)

	counts, err := q.CountByStatus("nonexistent")
	if err != nil {
		t.Fatalf("CountByStatus() returned error: %v", err)
	}
	if counts == nil {
		t.Fatal("expected empty map (not nil) for type with no jobs")
	}
	if len(counts) != 0 {
		t.Errorf("expected empty map, got %v", counts)
	}
}
