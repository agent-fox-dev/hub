package mergequeue

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ===========================================================================
// 8.1 — POST /merges submit endpoint tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-56: POST /merges infers campaign_id and spec_id from source_ref,
// populates submitted_by from GetAuthInfo, generates nonce, inserts with
// status=prepared, and returns HTTP 202 with full job JSON excluding nonce.
// Requirement: 11-REQ-10.1
// ---------------------------------------------------------------------------

func TestHandlerSubmit_CampaignInference_Returns202(t *testing.T) {
	env := newMergeHTTPTestEnvWithCampaigns(t)
	now := time.Now().UTC().Format(time.RFC3339)

	// Insert an active campaign whose spec branch pattern matches source_ref.
	campaignID := newTestUUID("camp1")
	insertTestCampaign(t, env.db, campaignID, "my-workspace", "main", `{"07":[]}`, now)
	insertTestCampaignSpec(t, env.db, campaignID, "07", "pending", "abc123", now)

	userID := newTestUUID("user1")
	auth := mergeWriteAuth(userID)

	body := `{"target_branch":"main","source_ref":"spec/07-secrets-variables"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	// Must return HTTP 202 Accepted.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// campaign_id must be non-null (inferred from source_ref).
	if respBody["campaign_id"] == nil {
		t.Error("campaign_id is null; want non-null (inferred from source_ref matching active campaign)")
	}

	// spec_id must be "07" (extracted from "spec/07-secrets-variables").
	if specID, ok := respBody["spec_id"].(string); !ok || specID != "07" {
		t.Errorf("spec_id = %v; want '07'", respBody["spec_id"])
	}

	// submitted_by must match the authenticated user's UUID.
	if submittedBy, ok := respBody["submitted_by"].(string); !ok || submittedBy != userID {
		t.Errorf("submitted_by = %v; want %q", respBody["submitted_by"], userID)
	}

	// nonce must NOT appear in the response.
	if _, hasNonce := respBody["nonce"]; hasNonce {
		t.Error("response body contains 'nonce' field; want nonce excluded from API response")
	}

	// status must be "queued" (prepared -> enqueued -> queued).
	if status, ok := respBody["status"].(string); !ok || status != "queued" {
		t.Errorf("status = %v; want 'queued'", respBody["status"])
	}

	// Must have an id field.
	jobID, ok := respBody["id"].(string)
	if !ok || jobID == "" {
		t.Fatal("response body has no 'id' field or it is empty")
	}

	// Verify the DB record has a server-generated nonce.
	dbNonce := getJobNonce(t, env.db, jobID)
	if dbNonce == "" {
		t.Error("DB nonce is empty; want server-generated UUID")
	}
}

// ---------------------------------------------------------------------------
// TS-11-57: POST /merges accepts a standalone merge with campaign_id=null
// and spec_id=null when source_ref does not match any active campaign.
// Requirement: 11-REQ-10.2
// ---------------------------------------------------------------------------

func TestHandlerSubmit_StandaloneMerge_NullCampaign(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user2"))

	// source_ref "feature/my-branch" does not match spec/<id>-<name> pattern.
	body := `{"target_branch":"main","source_ref":"feature/my-branch"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// campaign_id must be null for standalone merges.
	if respBody["campaign_id"] != nil {
		t.Errorf("campaign_id = %v; want null for standalone merge", respBody["campaign_id"])
	}

	// spec_id must be null for standalone merges.
	if respBody["spec_id"] != nil {
		t.Errorf("spec_id = %v; want null for standalone merge", respBody["spec_id"])
	}

	// Status must still be "queued".
	if status, ok := respBody["status"].(string); !ok || status != "queued" {
		t.Errorf("status = %v; want 'queued'", respBody["status"])
	}
}

// ===========================================================================
// 8.2 — POST /merges edge-case tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-58: POST /merges returns HTTP 400 when the request body is missing
// target_branch or source_ref.
// Requirement: 11-REQ-10.E1
// ---------------------------------------------------------------------------

func TestHandlerSubmit_MissingSourceRef_Returns400(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user3"))

	// Missing source_ref.
	body := `{"target_branch":"main"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// The error envelope must contain an 'error' key.
	errVal := respBody["error"]
	if errVal == nil {
		t.Fatal("response body does not contain 'error' key; want apikit error body")
	}

	// Error message should mention the missing field.
	errMsg := fmt.Sprintf("%v", errVal)
	if errMsg == "" {
		t.Error("error message is empty")
	}
}

func TestHandlerSubmit_MissingTargetBranch_Returns400(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user3b"))

	// Missing target_branch.
	body := `{"source_ref":"spec/07"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandlerSubmit_EmptyBody_Returns400(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user3c"))

	body := `{}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-11-59: POST /merges returns HTTP 401 or 403 when the caller is
// unauthenticated or lacks merges:write scope.
// Requirement: 11-REQ-10.E2
// ---------------------------------------------------------------------------

func TestHandlerSubmit_Unauthenticated_Returns401(t *testing.T) {
	env := newMergeHTTPTestEnv(t)

	body := `{"target_branch":"main","source_ref":"spec/07"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, nil)

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 401 or 403; body = %s",
			rec.Code, rec.Body.String())
	}
}

func TestHandlerSubmit_ReadScopeOnly_Returns403(t *testing.T) {
	env := newMergeHTTPTestEnv(t)

	// PAT with merges:read (not merges:write).
	auth := mergeReadAuth(newTestUUID("user4"))

	body := `{"target_branch":"main","source_ref":"spec/07"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-11-60: POST /merges returns HTTP 404 when the workspace identified by
// :slug does not exist.
// Requirement: 11-REQ-10.E3
// ---------------------------------------------------------------------------

func TestHandlerSubmit_NonexistentWorkspace_Returns404(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user5"))

	body := `{"target_branch":"main","source_ref":"spec/07"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/nonexistent-workspace/merges", body, auth)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if _, hasError := respBody["error"]; !hasError {
		t.Error("response body does not contain 'error' key; want apikit error body")
	}
}

// ---------------------------------------------------------------------------
// TS-11-61: POST /merges ignores caller-supplied submitted_by and nonce
// fields; submitted_by is always from GetAuthInfo and nonce is always
// server-generated.
// Requirement: 11-REQ-10.E4
// ---------------------------------------------------------------------------

func TestHandlerSubmit_IgnoresCallerSubmittedByAndNonce(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	realUserID := newTestUUID("real1")
	auth := mergeWriteAuth(realUserID)

	// Include attacker-supplied submitted_by and nonce in the request.
	body := `{
		"target_branch":"main",
		"source_ref":"spec/07",
		"submitted_by":"attacker-uuid",
		"nonce":"attacker-nonce"
	}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// submitted_by must be the real authenticated user, not the attacker value.
	if submittedBy, ok := respBody["submitted_by"].(string); !ok || submittedBy != realUserID {
		t.Errorf("submitted_by = %v; want %q (must be from GetAuthInfo, not request body)",
			respBody["submitted_by"], realUserID)
	}

	// nonce must NOT appear in the response.
	if _, hasNonce := respBody["nonce"]; hasNonce {
		t.Error("response contains 'nonce' field; want nonce excluded from all API responses")
	}

	// DB nonce must NOT be the caller-supplied value.
	jobID, ok := respBody["id"].(string)
	if !ok || jobID == "" {
		t.Fatal("response has no 'id'; cannot verify DB nonce")
	}
	dbNonce := getJobNonce(t, env.db, jobID)
	if dbNonce == "attacker-nonce" {
		t.Error("DB nonce == 'attacker-nonce'; want server-generated nonce (caller value must be ignored)")
	}
}

// ===========================================================================
// 8.3 — GET /merges list endpoint — basic and pagination
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-62: GET /merges returns merge jobs ordered by (created_at ASC, id ASC)
// up to limit (default 50) in a pagination envelope with next_cursor;
// check_output is omitted from each item.
// Requirement: 11-REQ-11.1
// ---------------------------------------------------------------------------

func TestHandlerList_ReturnsOrderedJobsWithEnvelope(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader1"))

	// Insert 5 jobs with staggered created_at to verify ordering.
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		ts := baseTime.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		job := &MergeJob{
			ID:            fmt.Sprintf("list-%02d-id", i),
			Nonce:         fmt.Sprintf("list-%02d-nonce", i),
			WorkspaceSlug: "my-workspace",
			TargetBranch:  "main",
			SourceRef:     fmt.Sprintf("spec/%02d", i),
			Status:        "queued",
			RetryCount:    0,
			AvailableAt:   ts,
			SubmittedBy:   newTestUUID("user"),
			CreatedAt:     ts,
			UpdatedAt:     ts,
			CheckOutput:   sql.NullString{String: "some check output", Valid: true},
		}
		insertTestMergeJobFull(t, env.db, job)
	}

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var respBody struct {
		Items      []map[string]interface{} `json:"items"`
		NextCursor *string                  `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// Must return all 5 items.
	if len(respBody.Items) != 5 {
		t.Fatalf("items count = %d; want 5", len(respBody.Items))
	}

	// Verify ordering: (created_at, id) ascending.
	for i := 1; i < len(respBody.Items); i++ {
		prevCreated, _ := respBody.Items[i-1]["created_at"].(string)
		currCreated, _ := respBody.Items[i]["created_at"].(string)
		prevID, _ := respBody.Items[i-1]["id"].(string)
		currID, _ := respBody.Items[i]["id"].(string)

		if currCreated < prevCreated || (currCreated == prevCreated && currID <= prevID) {
			t.Errorf("items[%d] (%s, %s) is not after items[%d] (%s, %s); want (created_at, id) ASC ordering",
				i, currCreated, currID, i-1, prevCreated, prevID)
		}
	}

	// check_output must be omitted from each item.
	for i, item := range respBody.Items {
		if _, hasCheckOutput := item["check_output"]; hasCheckOutput {
			t.Errorf("items[%d] contains 'check_output'; want it omitted from list response", i)
		}
	}

	// next_cursor must be null since we have < 50 items.
	if respBody.NextCursor != nil {
		t.Errorf("next_cursor = %v; want null (all items fit on one page)", *respBody.NextCursor)
	}
}

// ---------------------------------------------------------------------------
// TS-11-63: GET /merges with an 'after' cursor returns only jobs where
// (created_at, id) > (after_created_at, after_id).
// Requirement: 11-REQ-11.2
// ---------------------------------------------------------------------------

func TestHandlerList_CursorPagination(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader2"))

	// Insert 10 jobs.
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		ts := baseTime.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		job := &MergeJob{
			ID:            fmt.Sprintf("page-%02d-id", i),
			Nonce:         fmt.Sprintf("page-%02d-nonce", i),
			WorkspaceSlug: "my-workspace",
			TargetBranch:  "main",
			SourceRef:     fmt.Sprintf("spec/%02d-page", i),
			Status:        "queued",
			RetryCount:    0,
			AvailableAt:   ts,
			SubmittedBy:   newTestUUID("user"),
			CreatedAt:     ts,
			UpdatedAt:     ts,
		}
		insertTestMergeJobFull(t, env.db, job)
	}

	// First page: limit=5.
	rec1 := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges?limit=5", "", auth)

	if rec1.Code != http.StatusOK {
		t.Fatalf("first page status = %d; want %d", rec1.Code, http.StatusOK)
	}

	var firstPage struct {
		Items      []map[string]interface{} `json:"items"`
		NextCursor *string                  `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec1.Body).Decode(&firstPage); err != nil {
		t.Fatalf("failed to decode first page: %v", err)
	}

	if len(firstPage.Items) != 5 {
		t.Fatalf("first page items = %d; want 5", len(firstPage.Items))
	}

	// next_cursor must be non-null since there are more pages.
	if firstPage.NextCursor == nil {
		t.Fatal("first page next_cursor is null; want non-null (more pages exist)")
	}

	// Second page: after=<cursor>&limit=5.
	rec2 := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges?after="+*firstPage.NextCursor+"&limit=5", "", auth)

	if rec2.Code != http.StatusOK {
		t.Fatalf("second page status = %d; want %d", rec2.Code, http.StatusOK)
	}

	var secondPage struct {
		Items      []map[string]interface{} `json:"items"`
		NextCursor *string                  `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec2.Body).Decode(&secondPage); err != nil {
		t.Fatalf("failed to decode second page: %v", err)
	}

	if len(secondPage.Items) != 5 {
		t.Fatalf("second page items = %d; want 5", len(secondPage.Items))
	}

	// Ensure no overlap between pages.
	firstPageIDs := make(map[string]bool)
	for _, item := range firstPage.Items {
		if id, ok := item["id"].(string); ok {
			firstPageIDs[id] = true
		}
	}
	for _, item := range secondPage.Items {
		if id, ok := item["id"].(string); ok {
			if firstPageIDs[id] {
				t.Errorf("second page contains item %q from first page; want no overlap", id)
			}
		}
	}

	// Second page's next_cursor should be null (no more pages).
	if secondPage.NextCursor != nil {
		t.Errorf("second page next_cursor = %v; want null (last page)", *secondPage.NextCursor)
	}
}

// ===========================================================================
// 8.4 — GET /merges list endpoint — filtering and response shape
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-64: GET /merges with a valid status filter returns only jobs matching
// that status.
// Requirement: 11-REQ-11.3
// ---------------------------------------------------------------------------

func TestHandlerList_StatusFilter(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader3"))

	now := time.Now().UTC().Format(time.RFC3339)

	// Insert 3 merged jobs and 2 queued jobs.
	for i := 0; i < 3; i++ {
		job := &MergeJob{
			ID:            fmt.Sprintf("filt-merged-%d", i),
			Nonce:         fmt.Sprintf("filt-merged-n-%d", i),
			WorkspaceSlug: "my-workspace",
			TargetBranch:  "main",
			SourceRef:     fmt.Sprintf("spec/%02d-merged", i),
			Status:        "merged",
			RetryCount:    0,
			AvailableAt:   now,
			SubmittedBy:   newTestUUID("user"),
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		insertTestMergeJobFull(t, env.db, job)
	}
	for i := 0; i < 2; i++ {
		job := &MergeJob{
			ID:            fmt.Sprintf("filt-queued-%d", i),
			Nonce:         fmt.Sprintf("filt-queued-n-%d", i),
			WorkspaceSlug: "my-workspace",
			TargetBranch:  "main",
			SourceRef:     fmt.Sprintf("spec/%02d-queued", i),
			Status:        "queued",
			RetryCount:    0,
			AvailableAt:   now,
			SubmittedBy:   newTestUUID("user"),
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		insertTestMergeJobFull(t, env.db, job)
	}

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges?status=merged", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var respBody struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// Must return exactly 3 merged items.
	if len(respBody.Items) != 3 {
		t.Fatalf("items count = %d; want 3", len(respBody.Items))
	}

	for i, item := range respBody.Items {
		if status, ok := item["status"].(string); !ok || status != "merged" {
			t.Errorf("items[%d].status = %v; want 'merged'", i, item["status"])
		}
	}
}

// ---------------------------------------------------------------------------
// TS-11-65: GET /merges sets next_cursor to null when there are no further
// pages beyond the current result set.
// Requirement: 11-REQ-11.4
// ---------------------------------------------------------------------------

func TestHandlerList_NextCursorNullWhenNoMorePages(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader4"))

	now := time.Now().UTC().Format(time.RFC3339)

	// Insert exactly 3 jobs.
	for i := 0; i < 3; i++ {
		job := &MergeJob{
			ID:            fmt.Sprintf("nc-%02d-id", i),
			Nonce:         fmt.Sprintf("nc-%02d-nonce", i),
			WorkspaceSlug: "my-workspace",
			TargetBranch:  "main",
			SourceRef:     fmt.Sprintf("spec/%02d-nc", i),
			Status:        "queued",
			RetryCount:    0,
			AvailableAt:   now,
			SubmittedBy:   newTestUUID("user"),
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		insertTestMergeJobFull(t, env.db, job)
	}

	// Request with limit=10 (more than total count).
	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges?limit=10", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var respBody struct {
		Items      []map[string]interface{} `json:"items"`
		NextCursor *string                  `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if len(respBody.Items) != 3 {
		t.Fatalf("items count = %d; want 3", len(respBody.Items))
	}

	if respBody.NextCursor != nil {
		t.Errorf("next_cursor = %v; want null (all items fit on one page)", *respBody.NextCursor)
	}
}

// TestHandlerList_CheckOutputOmitted verifies that check_output is excluded
// from each item in the list response, even when it has a value in the DB.
func TestHandlerList_CheckOutputOmitted(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader5"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "co-omit-id",
		Nonce:         "co-omit-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/01-check-output",
		Status:        "check_failed",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
		CheckOutput:   sql.NullString{String: "FAIL: tests did not pass", Valid: true},
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	var respBody struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if len(respBody.Items) != 1 {
		t.Fatalf("items count = %d; want 1", len(respBody.Items))
	}

	if _, hasCheckOutput := respBody.Items[0]["check_output"]; hasCheckOutput {
		t.Error("list item contains 'check_output'; want it omitted from list response")
	}
}

// TestHandlerList_ConflictDetailsAsJSONArray verifies that conflict_details
// appears in the list response as a native JSON array, not a JSON-encoded
// string.
func TestHandlerList_ConflictDetailsAsJSONArray(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader6"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:              "cd-arr-id",
		Nonce:           "cd-arr-nonce",
		WorkspaceSlug:   "my-workspace",
		TargetBranch:    "main",
		SourceRef:       "spec/02-conflict",
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
		"/api/v1/workspaces/my-workspace/merges", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	var respBody struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if len(respBody.Items) != 1 {
		t.Fatalf("items count = %d; want 1", len(respBody.Items))
	}

	cd := respBody.Items[0]["conflict_details"]
	if cd == nil {
		t.Fatal("conflict_details is nil; want a JSON array")
	}

	// Must be a JSON array, not a string.
	switch v := cd.(type) {
	case []interface{}:
		if len(v) != 2 {
			t.Errorf("conflict_details array length = %d; want 2", len(v))
		}
	case string:
		t.Errorf("conflict_details is a string %q; want a native JSON array (deserialized from TEXT)", v)
	default:
		t.Errorf("conflict_details is type %T; want []interface{} (JSON array)", cd)
	}
}

// ===========================================================================
// 8.5 — GET /merges list endpoint — edge cases
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-66: GET /merges returns HTTP 400 with an apikit error body listing
// the invalid value and valid options when status parameter is unrecognized.
// Requirement: 11-REQ-11.E1
// ---------------------------------------------------------------------------

func TestHandlerList_InvalidStatus_Returns400(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader7"))

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges?status=invalid_status", "", auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// Must contain an error field.
	errField := respBody["error"]
	if errField == nil {
		t.Fatal("response body does not contain 'error' key; want apikit error body")
	}

	// The error message should contain the invalid value.
	errStr := fmt.Sprintf("%v", errField)
	if errStr == "" {
		t.Error("error message is empty")
	}

	// The error message should contain the invalid status name.
	// Re-serialize decoded body for substring checks (Decode consumed the buffer).
	bodyBytes, _ := json.Marshal(respBody)
	bodyStr := string(bodyBytes)
	if !bodyContains(bodyStr, "invalid_status") {
		t.Errorf("error body does not contain 'invalid_status'; want the invalid value to be listed; got: %s", bodyStr)
	}

	// Should list at least some valid status values.
	hasValidStatus := false
	for _, validStatus := range []string{"queued", "merged", "conflict", "dead_letter"} {
		if bodyContains(bodyStr, validStatus) {
			hasValidStatus = true
			break
		}
	}
	if !hasValidStatus {
		t.Errorf("error body does not list any valid status values; want valid options to be included; got: %s", bodyStr)
	}
}

// ---------------------------------------------------------------------------
// TS-11-67: GET /merges returns HTTP 400 when the limit parameter exceeds 100.
// Requirement: 11-REQ-11.E2
// ---------------------------------------------------------------------------

func TestHandlerList_LimitExceeds100_Returns400(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader8"))

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges?limit=101", "", auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if _, hasError := respBody["error"]; !hasError {
		t.Error("response body does not contain 'error' key; want apikit error body")
	}
}

// ---------------------------------------------------------------------------
// TS-11-68: GET /merges returns HTTP 400 when the 'after' cursor references
// a job ID that does not exist.
// Requirement: 11-REQ-11.E3
// ---------------------------------------------------------------------------

func TestHandlerList_InvalidCursor_Returns400(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader9"))

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges?after=nonexistent-uuid", "", auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if _, hasError := respBody["error"]; !hasError {
		t.Error("response body does not contain 'error' key; want apikit error body")
	}
}

// ---------------------------------------------------------------------------
// TS-11-69: GET /merges returns HTTP 401 or 403 when the caller is
// unauthenticated or lacks merges:read scope.
// Requirement: 11-REQ-11.E4
// ---------------------------------------------------------------------------

func TestHandlerList_Unauthenticated_Returns401(t *testing.T) {
	env := newMergeHTTPTestEnv(t)

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges", "", nil)

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 401 or 403; body = %s",
			rec.Code, rec.Body.String())
	}
}

func TestHandlerList_WriteScopeOnly_Returns403(t *testing.T) {
	env := newMergeHTTPTestEnv(t)

	// PAT with merges:write (not merges:read) — should be rejected for GET.
	auth := mergeWriteAuth(newTestUUID("reader10"))

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-11-70: GET /merges returns HTTP 200 with an empty items array and
// next_cursor=null when no jobs match the filter.
// Requirement: 11-REQ-11.E5
// ---------------------------------------------------------------------------

func TestHandlerList_EmptyResult_Returns200WithEmptyItems(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader11"))

	// No jobs exist with status=conflict.
	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges?status=conflict", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var respBody struct {
		Items      []interface{} `json:"items"`
		NextCursor *string       `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if respBody.Items == nil {
		t.Fatal("items is nil; want empty array")
	}

	if len(respBody.Items) != 0 {
		t.Errorf("items count = %d; want 0", len(respBody.Items))
	}

	if respBody.NextCursor != nil {
		t.Errorf("next_cursor = %v; want null", *respBody.NextCursor)
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

// bodyContains checks if the response body string contains the given substring.
func bodyContains(body, substring string) bool {
	return strings.Contains(body, substring)
}
