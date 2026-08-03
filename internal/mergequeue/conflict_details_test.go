package mergequeue

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ===========================================================================
// 10.1 — conflict_details Serialization Boundary Tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-85: The HTTP handler unmarshals conflict_details TEXT from the store
// into a native JSON array before responding; clients receive a proper array,
// never a JSON-encoded string.
// Requirement: 11-REQ-15.1
// ---------------------------------------------------------------------------

func TestConflictDetails_NativeJSONArray_NotEncodedString(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader-cd1"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:              newTestUUID("cd1"),
		Nonce:           newTestUUID("ncd1"),
		WorkspaceSlug:   "my-workspace",
		TargetBranch:    "main",
		SourceRef:       "spec/07-secrets-variables",
		Status:          "conflict",
		RetryCount:      0,
		AvailableAt:     now,
		SubmittedBy:     newTestUUID("user"),
		CreatedAt:       now,
		UpdatedAt:       now,
		ConflictDetails: sql.NullString{String: `["file1.go","file2.go"]`, Valid: true},
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/"+job.ID, "", auth)

	// Must return HTTP 200.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// conflict_details must be present.
	cd := respBody["conflict_details"]
	if cd == nil {
		t.Fatal("conflict_details is nil; want a JSON array of file paths")
	}

	// conflict_details must be a native JSON array, NOT a JSON-encoded string.
	switch v := cd.(type) {
	case []interface{}:
		// Correct: native JSON array.
		if len(v) != 2 {
			t.Errorf("conflict_details array length = %d; want 2", len(v))
		}

		// Verify the array contains the expected file paths.
		wantFiles := map[string]bool{"file1.go": false, "file2.go": false}
		for _, item := range v {
			if file, ok := item.(string); ok {
				wantFiles[file] = true
			}
		}
		for file, found := range wantFiles {
			if !found {
				t.Errorf("conflict_details does not contain %q", file)
			}
		}
	case string:
		t.Errorf("conflict_details is a string %q; want a native JSON array (the handler must unmarshal the TEXT column)", v)
	default:
		t.Errorf("conflict_details is type %T; want []interface{} (native JSON array)", cd)
	}
}

// ---------------------------------------------------------------------------
// TS-11-86: When no conflicts were recorded (conflict_details is NULL in DB),
// the HTTP handler serializes conflict_details as null in the response.
// Requirement: 11-REQ-15.2
// ---------------------------------------------------------------------------

func TestConflictDetails_NullWhenNoConflicts(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader-cd2"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            newTestUUID("cd2"),
		Nonce:         newTestUUID("ncd2"),
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/08-something",
		Status:        "merged",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
		// ConflictDetails is zero-value (NULL in DB).
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/"+job.ID, "", auth)

	// Must return HTTP 200.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Parse response as raw JSON to distinguish null from absent.
	var rawResp map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rawResp); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	cdRaw, exists := rawResp["conflict_details"]
	if !exists {
		t.Fatal("conflict_details key is absent from response; want it present with value null")
	}

	// The raw JSON for null is literally "null".
	if string(cdRaw) != "null" {
		t.Errorf("conflict_details = %s; want null", string(cdRaw))
	}
}

// ---------------------------------------------------------------------------
// TS-11-87: When conflict_details TEXT contains malformed JSON, the handler
// logs the parse error with merge_job_id and returns null for conflict_details
// rather than HTTP 500.
// Requirement: 11-REQ-15.E1
// ---------------------------------------------------------------------------

func TestConflictDetails_MalformedJSON_ReturnsNullNotHTTP500(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader-cd3"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:              newTestUUID("cd3"),
		Nonce:           newTestUUID("ncd3"),
		WorkspaceSlug:   "my-workspace",
		TargetBranch:    "main",
		SourceRef:       "spec/09-something",
		Status:          "conflict",
		RetryCount:      0,
		AvailableAt:     now,
		SubmittedBy:     newTestUUID("user"),
		CreatedAt:       now,
		UpdatedAt:       now,
		ConflictDetails: sql.NullString{String: "not-valid-json", Valid: true},
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/"+job.ID, "", auth)

	// Must return HTTP 200 (NOT 500).
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d (malformed conflict_details must not cause HTTP 500); body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Parse response as raw JSON.
	var rawResp map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rawResp); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	cdRaw, exists := rawResp["conflict_details"]
	if !exists {
		t.Fatal("conflict_details key is absent from response; want it present with value null")
	}

	// Malformed JSON in TEXT column must result in null in the response.
	if string(cdRaw) != "null" {
		t.Errorf("conflict_details = %s; want null (malformed JSON in DB should be treated as null)", string(cdRaw))
	}
}
