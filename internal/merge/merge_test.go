package merge

import (
	"encoding/json"
	"testing"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
	"github.com/txsvc/apikit"
)

// ---------------------------------------------------------------------------
// TS-12-6: The hub server registers a job handler for type 'merge' with the
// durable job queue before serving the first request.
//
// Requirement: 12-REQ-2.1
// ---------------------------------------------------------------------------

func TestRegisterHandler_MergeType(t *testing.T) {
	q, _ := newTestQueue(t)

	err := RegisterHandler(q, nil)
	if err != nil {
		t.Fatalf("RegisterHandler() returned error: %v", err)
	}

	// Verify registration by enqueueing a merge job. If the type is not
	// registered, Enqueue returns an error ("type not registered").
	jobID, _, enqErr := q.Enqueue(jobqueue.EnqueueParams{
		Type:        "merge",
		Key:         "test:main:feature/x",
		Nonce:       "ts12-6-nonce",
		Payload:     json.RawMessage(`{}`),
		SubmittedBy: "test",
	})
	if enqErr != nil {
		t.Fatalf("Enqueue('merge') failed after RegisterHandler: %v", enqErr)
	}
	if jobID == "" {
		t.Error("expected non-empty job ID after successful merge enqueue")
	}
}

// ---------------------------------------------------------------------------
// TS-12-6 Edge Case (12-REQ-2.E1): Registering the 'merge' job type more
// than once returns an error to prevent silent misconfiguration.
// ---------------------------------------------------------------------------

func TestRegisterHandler_DuplicateRegistrationFails(t *testing.T) {
	q, _ := newTestQueue(t)

	err := RegisterHandler(q, nil)
	if err != nil {
		t.Fatalf("first RegisterHandler() returned error: %v", err)
	}

	// Second registration must fail.
	err = RegisterHandler(q, nil)
	if err == nil {
		t.Fatal("expected error on duplicate RegisterHandler(), got nil")
	}
}

// ---------------------------------------------------------------------------
// TS-12-7: Every merge job is enqueued with
// Key='<workspace_slug>:<target_branch>:<source_ref>' and
// Group='<workspace_slug>:<target_branch>'.
//
// Requirement: 12-REQ-2.2
// ---------------------------------------------------------------------------

func TestEnqueueMergeJob_KeyAndGroupFormat(t *testing.T) {
	q, db := newTestQueue(t)

	// Register the merge handler so the queue accepts merge-type jobs.
	if err := RegisterHandler(q, nil); err != nil {
		t.Fatalf("RegisterHandler() returned error: %v", err)
	}

	jobID, _, err := EnqueueMergeJob(q, "my-ws", "main", "feature/x", "alice")
	if err != nil {
		t.Fatalf("EnqueueMergeJob() returned error: %v", err)
	}
	if jobID == "" {
		t.Fatal("EnqueueMergeJob() returned empty job ID")
	}

	// Verify the Key format: '<workspace_slug>:<target_branch>:<source_ref>'
	var key, groupKey string
	queryErr := db.QueryRow("SELECT key, group_key FROM jobs WHERE id = ?", jobID).Scan(&key, &groupKey)
	if queryErr != nil {
		t.Fatalf("query job row failed: %v", queryErr)
	}
	if key != "my-ws:main:feature/x" {
		t.Errorf("expected key='my-ws:main:feature/x', got %q", key)
	}

	// Verify the Group format: '<workspace_slug>:<target_branch>'
	if groupKey != "my-ws:main" {
		t.Errorf("expected group_key='my-ws:main', got %q", groupKey)
	}
}

// ---------------------------------------------------------------------------
// TS-12-8: When a merge job is enqueued via an API key, submitted_by in the
// job payload equals the API key owner's username from AuthInfo.
//
// Requirement: 12-REQ-3.1
// ---------------------------------------------------------------------------

func TestEnqueueMergeJob_SubmittedByAPIKey(t *testing.T) {
	q, db := newTestQueue(t)

	if err := RegisterHandler(q, nil); err != nil {
		t.Fatalf("RegisterHandler() returned error: %v", err)
	}

	// Simulate API key authentication. AuthInfo.UserID holds the username
	// (see errata 01_apikit_auth_exports.md).
	auth := &apikit.AuthInfo{
		CredentialType: "api_key",
		UserID:         "alice",
	}

	submittedBy, err := ResolveSubmittedBy(auth)
	if err != nil {
		t.Fatalf("ResolveSubmittedBy() returned error: %v", err)
	}
	if submittedBy != "alice" {
		t.Errorf("expected submitted_by='alice', got %q", submittedBy)
	}

	// Enqueue and verify payload contains submitted_by.
	jobID, _, enqErr := EnqueueMergeJob(q, "ws1", "main", "feature/a", submittedBy)
	if enqErr != nil {
		t.Fatalf("EnqueueMergeJob() returned error: %v", enqErr)
	}

	var payloadStr string
	if err := db.QueryRow("SELECT payload FROM jobs WHERE id = ?", jobID).Scan(&payloadStr); err != nil {
		t.Fatalf("query payload failed: %v", err)
	}

	var payload MergePayload
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}
	if payload.SubmittedBy != "alice" {
		t.Errorf("expected payload.submitted_by='alice', got %q", payload.SubmittedBy)
	}
}

// ---------------------------------------------------------------------------
// TS-12-9: When a merge job is enqueued via an admin token, submitted_by in
// the job payload is the literal string 'admin'.
//
// Requirement: 12-REQ-3.2
// ---------------------------------------------------------------------------

func TestEnqueueMergeJob_SubmittedByAdminToken(t *testing.T) {
	q, db := newTestQueue(t)

	if err := RegisterHandler(q, nil); err != nil {
		t.Fatalf("RegisterHandler() returned error: %v", err)
	}

	// Admin tokens have CredentialType="admin_token" and empty UserID.
	auth := &apikit.AuthInfo{
		CredentialType: "admin_token",
	}

	submittedBy, err := ResolveSubmittedBy(auth)
	if err != nil {
		t.Fatalf("ResolveSubmittedBy() returned error: %v", err)
	}
	if submittedBy != "admin" {
		t.Errorf("expected submitted_by='admin', got %q", submittedBy)
	}

	// Enqueue and verify payload contains submitted_by = "admin".
	jobID, _, enqErr := EnqueueMergeJob(q, "ws1", "main", "feature/a", submittedBy)
	if enqErr != nil {
		t.Fatalf("EnqueueMergeJob() returned error: %v", enqErr)
	}

	var payloadStr string
	if err := db.QueryRow("SELECT payload FROM jobs WHERE id = ?", jobID).Scan(&payloadStr); err != nil {
		t.Fatalf("query payload failed: %v", err)
	}

	var payload MergePayload
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}
	if payload.SubmittedBy != "admin" {
		t.Errorf("expected payload.submitted_by='admin', got %q", payload.SubmittedBy)
	}
}

// ---------------------------------------------------------------------------
// TS-12-10: When a merge job is enqueued via a PAT, submitted_by in the
// job payload equals the PAT owner's username from AuthInfo.
//
// Requirement: 12-REQ-3.3
// ---------------------------------------------------------------------------

func TestEnqueueMergeJob_SubmittedByPAT(t *testing.T) {
	q, db := newTestQueue(t)

	if err := RegisterHandler(q, nil); err != nil {
		t.Fatalf("RegisterHandler() returned error: %v", err)
	}

	// PAT authentication with the PAT owner's UserID.
	auth := &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         "bob",
		Permissions:    []string{"merges:write"},
	}

	submittedBy, err := ResolveSubmittedBy(auth)
	if err != nil {
		t.Fatalf("ResolveSubmittedBy() returned error: %v", err)
	}
	if submittedBy != "bob" {
		t.Errorf("expected submitted_by='bob', got %q", submittedBy)
	}

	// Enqueue and verify payload contains submitted_by = "bob".
	jobID, _, enqErr := EnqueueMergeJob(q, "ws1", "main", "feature/a", submittedBy)
	if enqErr != nil {
		t.Fatalf("EnqueueMergeJob() returned error: %v", enqErr)
	}

	var payloadStr string
	if err := db.QueryRow("SELECT payload FROM jobs WHERE id = ?", jobID).Scan(&payloadStr); err != nil {
		t.Fatalf("query payload failed: %v", err)
	}

	var payload MergePayload
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}
	if payload.SubmittedBy != "bob" {
		t.Errorf("expected payload.submitted_by='bob', got %q", payload.SubmittedBy)
	}
}

// ---------------------------------------------------------------------------
// TS-12-3.E1 Edge Case: If AuthInfo does not contain a resolvable username
// (e.g., nil AuthInfo), ResolveSubmittedBy returns an error.
//
// Requirement: 12-REQ-3.E1
// ---------------------------------------------------------------------------

func TestResolveSubmittedBy_NilAuthInfo(t *testing.T) {
	_, err := ResolveSubmittedBy(nil)
	if err == nil {
		t.Fatal("expected error for nil AuthInfo, got nil")
	}
}
