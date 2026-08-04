package jobqueue

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-7: Enqueue with a registered type, unique nonce, and no active job
// for the same (type, key) inserts a queued job and returns a non-empty job ID
// with duplicate=false and err=nil.
// Requirement: 10-REQ-3.1
// ---------------------------------------------------------------------------

func TestEnqueue_ValidJob(t *testing.T) {
	queue, db := newTestQueue(t)
	registerTestHandler(t, queue, "merge")

	jobID, dup, err := queue.Enqueue(EnqueueParams{
		Type:        "merge",
		Key:         "main",
		Nonce:       "abc123",
		Payload:     json.RawMessage(`{"branch":"main"}`),
		SubmittedBy: "user-1",
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}
	if dup {
		t.Error("expected duplicate=false")
	}
	if jobID == "" {
		t.Fatal("expected non-empty job ID")
	}

	// Verify the database row.
	var status, availableAt string
	err = db.QueryRow("SELECT status, available_at FROM jobs WHERE id=?", jobID).Scan(&status, &availableAt)
	if err != nil {
		t.Fatalf("query job by ID failed: %v", err)
	}
	if status != "queued" {
		t.Errorf("expected status='queued', got %q", status)
	}

	parsed, parseErr := time.Parse(time.RFC3339, availableAt)
	if parseErr != nil {
		t.Fatalf("failed to parse available_at %q: %v", availableAt, parseErr)
	}
	if time.Since(parsed) > 5*time.Second {
		t.Errorf("available_at should be approximately now, got %v (age=%v)", parsed, time.Since(parsed))
	}
}

// ---------------------------------------------------------------------------
// TS-10-8: Enqueue called with a nonce that matches an existing job returns
// the existing job's ID with duplicate=false and err=nil without inserting
// a new record.
// Requirement: 10-REQ-3.2
// Property: 10-PROP-2 (nonce uniqueness)
// ---------------------------------------------------------------------------

func TestEnqueue_DuplicateNonce(t *testing.T) {
	queue, db := newTestQueue(t)
	registerTestHandler(t, queue, "merge")

	// Seed a job with nonce='abc123'.
	seedJob(t, db, "existing-job-id", "merge", "main", "abc123", "queued")

	// Enqueue with the same nonce should return the existing job.
	jobID, dup, err := queue.Enqueue(EnqueueParams{
		Type:        "merge",
		Key:         "main",
		Nonce:       "abc123",
		Payload:     json.RawMessage(`{}`),
		SubmittedBy: "user-1",
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}
	if dup {
		t.Error("expected duplicate=false for nonce match (idempotent retransmission)")
	}
	if jobID != "existing-job-id" {
		t.Errorf("expected jobID='existing-job-id', got %q", jobID)
	}

	// Verify no new record was inserted.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM jobs WHERE nonce='abc123'").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 row with nonce='abc123', got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-10-9: Enqueue with a unique nonce but a (type, key) pair that already
// has an active queued job returns the existing job's ID with duplicate=true
// and err=nil.
// Requirement: 10-REQ-3.3
// Property: 10-PROP-1 (at-most-one-active-per-key)
// ---------------------------------------------------------------------------

func TestEnqueue_DuplicateTypeKey(t *testing.T) {
	queue, db := newTestQueue(t)
	registerTestHandler(t, queue, "merge")

	// Seed an active (queued) job for (type='merge', key='main').
	seedJob(t, db, "j1", "merge", "main", "original-nonce", "queued")

	// Enqueue with a different nonce but the same (type, key).
	jobID, dup, err := queue.Enqueue(EnqueueParams{
		Type:        "merge",
		Key:         "main",
		Nonce:       "new-nonce",
		Payload:     json.RawMessage(`{}`),
		SubmittedBy: "user-2",
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}
	if !dup {
		t.Error("expected duplicate=true for active (type, key) match")
	}
	if jobID != "j1" {
		t.Errorf("expected jobID='j1', got %q", jobID)
	}

	// Verify no new row was inserted.
	var count int
	err = db.QueryRow(
		"SELECT COUNT(*) FROM jobs WHERE type='merge' AND key='main' AND status IN ('queued','running')",
	).Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 active job for (merge, main), got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-10-10: Enqueue with an unregistered job type returns an empty string,
// false, and a non-nil error without inserting any record.
// Requirement: 10-REQ-3.4
// ---------------------------------------------------------------------------

func TestEnqueue_UnregisteredType(t *testing.T) {
	queue, db := newTestQueue(t)
	// Intentionally do NOT register 'unknown-type'.

	jobID, dup, err := queue.Enqueue(EnqueueParams{
		Type:        "unknown-type",
		Key:         "k",
		Nonce:       "n1",
		Payload:     json.RawMessage(`{}`),
		SubmittedBy: "sys",
	})
	if err == nil {
		t.Fatal("expected error for unregistered type, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "not registered") && !strings.Contains(errMsg, "unknown type") {
		t.Errorf("error should mention 'not registered' or 'unknown type', got: %q", errMsg)
	}
	if jobID != "" {
		t.Errorf("expected empty jobID, got %q", jobID)
	}
	if dup {
		t.Error("expected duplicate=false")
	}

	// Verify no row was inserted.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM jobs WHERE type='unknown-type'").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows for unregistered type, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-10-40: A second Enqueue call with the same nonce returns the existing
// job's ID without inserting a new record, enforced by the unique index
// on nonce. Tests nonce collision with a different (type, key).
// Requirement: 10-REQ-13.1
// ---------------------------------------------------------------------------

func TestEnqueue_NonceCollisionDifferentKey(t *testing.T) {
	queue, db := newTestQueue(t)
	registerTestHandler(t, queue, "merge")

	// Seed a job with nonce='n1'.
	seedJob(t, db, "j1", "merge", "main", "n1", "queued")

	// Enqueue with the same nonce but a different key.
	jobID, dup, err := queue.Enqueue(EnqueueParams{
		Type:        "merge",
		Key:         "other-key",
		Nonce:       "n1",
		Payload:     json.RawMessage(`{}`),
		SubmittedBy: "sys",
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}
	if jobID != "j1" {
		t.Errorf("expected jobID='j1', got %q", jobID)
	}
	if dup {
		t.Error("expected duplicate=false for nonce match (not a type+key duplicate)")
	}

	// Verify no new record was inserted.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM jobs WHERE nonce='n1'").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 row with nonce='n1', got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-10-41: Nonce deduplication and (type, key) duplicate prevention are
// independent checks: a nonce match returns the existing job regardless of
// its current status, while (type, key) check only applies to active jobs.
// Requirement: 10-REQ-13.2
// ---------------------------------------------------------------------------

func TestEnqueue_NonceMatchRegardlessOfStatus(t *testing.T) {
	queue, db := newTestQueue(t)
	registerTestHandler(t, queue, "merge")

	// Seed a COMPLETED job with nonce='n1'. The job is no longer active,
	// but nonce deduplication must still return it.
	seedJob(t, db, "j1", "merge", "main", "n1", "completed")

	jobID, dup, err := queue.Enqueue(EnqueueParams{
		Type:        "merge",
		Key:         "main",
		Nonce:       "n1",
		Payload:     json.RawMessage(`{}`),
		SubmittedBy: "sys",
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}
	if jobID != "j1" {
		t.Errorf("expected jobID='j1' (nonce match on completed job), got %q", jobID)
	}
	if dup {
		t.Error("expected duplicate=false for nonce match (not a type+key duplicate)")
	}

	// Verify no new job was inserted.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 total job, got %d", count)
	}
}
