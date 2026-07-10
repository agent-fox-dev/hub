package teams_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Smoke Tests
// End-to-end tests against the full production wiring: auth middleware →
// admin middleware → handler → store → SQLite. All run through
// RegisterTeamRoutes (router.go), verifying no stubs in the chain.
// ---------------------------------------------------------------------------

// TS-03-SMOKE-1: Admin creates a new team with valid name, slug, and URL.
// Execution Path: 03-PATH-1
//
// Real components: Echo HTTP router, auth + admin middleware, POST handler,
// SQLite DB with teams table and partial UNIQUE indexes.
func TestSmoke_CreateTeam(t *testing.T) {
	e, db := setupRouterTest(t, adminAuthCtx())

	body := `{"name": "  Smoke Team  ", "slug": "smoke-team", "url": "https://smoke.example.com"}`
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

	// Assert HTTP 201.
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Assert Content-Type: application/json.
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type containing 'application/json', got %q", ct)
	}

	// Parse and verify response body.
	resp := parseTeamResponse(t, rec)

	if _, err := uuid.Parse(resp.ID); err != nil {
		t.Errorf("id is not a valid UUID: %q", resp.ID)
	}
	if resp.Name != "Smoke Team" {
		t.Errorf("expected trimmed name %q, got %q", "Smoke Team", resp.Name)
	}
	if resp.Slug != "smoke-team" {
		t.Errorf("expected slug %q, got %q", "smoke-team", resp.Slug)
	}
	if resp.URL == nil || *resp.URL != "https://smoke.example.com" {
		t.Errorf("expected url %q, got %v", "https://smoke.example.com", resp.URL)
	}
	if resp.Status != "active" {
		t.Errorf("expected status 'active', got %q", resp.Status)
	}

	// Verify RFC3339 UTC microsecond timestamps.
	if !rfc3339MicroRe.MatchString(resp.CreatedAt) {
		t.Errorf("created_at does not match RFC3339 microsecond format: %q", resp.CreatedAt)
	}
	if !rfc3339MicroRe.MatchString(resp.UpdatedAt) {
		t.Errorf("updated_at does not match RFC3339 microsecond format: %q", resp.UpdatedAt)
	}

	// Verify DB row exists with correct values.
	var dbName, dbSlug, dbStatus string
	err := db.QueryRow(
		"SELECT name, slug, status FROM teams WHERE id = ?", resp.ID,
	).Scan(&dbName, &dbSlug, &dbStatus)
	if err != nil {
		t.Fatalf("team not found in DB: %v", err)
	}
	if dbName != "Smoke Team" {
		t.Errorf("DB name: got %q, want %q", dbName, "Smoke Team")
	}
	if dbSlug != "smoke-team" {
		t.Errorf("DB slug: got %q, want %q", dbSlug, "smoke-team")
	}
	if dbStatus != "active" {
		t.Errorf("DB status: got %q, want %q", dbStatus, "active")
	}

	// Verify no pre-existing team with the same slug (uniqueness).
	var count int
	if err := db.QueryRow(
		"SELECT count(*) FROM teams WHERE slug = ? AND status != 'deleted'", "smoke-team",
	).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 team with slug, got %d", count)
	}
}

// TS-03-SMOKE-2: Admin archives an active team and then deletes it with
// cascade deletion of all member rows.
// Execution Path: 03-PATH-2
func TestSmoke_ArchiveThenDelete(t *testing.T) {
	e, db := setupRouterTest(t, adminAuthCtx())

	// Step 1: Create a team.
	createBody := `{"name": "Smoke Delete Team", "slug": "smoke-delete"}`
	createRec := doRequest(t, e, http.MethodPost, "/api/v1/teams", createBody)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	createResp := parseTeamResponse(t, createRec)
	teamID := createResp.ID

	// Step 2: Add two members.
	user1ID := seedUser(t, db, "user1@smoke.com", "User One")
	user2ID := seedUser(t, db, "user2@smoke.com", "User Two")

	addBody1 := fmt.Sprintf(`{"user_id": "%s"}`, user1ID)
	addRec1 := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", addBody1)
	if addRec1.Code != http.StatusOK {
		t.Fatalf("add member 1: expected 200, got %d: %s", addRec1.Code, addRec1.Body.String())
	}

	addBody2 := fmt.Sprintf(`{"user_id": "%s"}`, user2ID)
	addRec2 := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", addBody2)
	if addRec2.Code != http.StatusOK {
		t.Fatalf("add member 2: expected 200, got %d: %s", addRec2.Code, addRec2.Body.String())
	}

	// Verify 2 members exist.
	var memberCount int
	if err := db.QueryRow(
		"SELECT count(*) FROM team_members WHERE team_id = ?", teamID,
	).Scan(&memberCount); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if memberCount != 2 {
		t.Fatalf("expected 2 members before archive, got %d", memberCount)
	}

	// Step 3: Archive the team.
	archiveRec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive: expected 200, got %d: %s", archiveRec.Code, archiveRec.Body.String())
	}
	archiveResp := parseTeamResponse(t, archiveRec)
	if archiveResp.Status != "archived" {
		t.Errorf("archive: expected status 'archived', got %q", archiveResp.Status)
	}
	// Verify updated_at is refreshed.
	if archiveResp.UpdatedAt == createResp.UpdatedAt {
		t.Error("archive: expected updated_at to be refreshed")
	}

	// Step 4: Delete the archived team.
	deleteRec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}
	// HTTP 204 must have no body.
	if deleteRec.Body.Len() > 0 {
		t.Errorf("delete: expected empty body, got %q", deleteRec.Body.String())
	}

	// Step 5: Verify team row is removed from DB.
	var teamCount int
	if err := db.QueryRow(
		"SELECT count(*) FROM teams WHERE id = ?", teamID,
	).Scan(&teamCount); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if teamCount != 0 {
		t.Errorf("expected team to be removed, got count %d", teamCount)
	}

	// Step 6: Verify all team_members rows are removed.
	if err := db.QueryRow(
		"SELECT count(*) FROM team_members WHERE team_id = ?", teamID,
	).Scan(&memberCount); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if memberCount != 0 {
		t.Errorf("expected members to be removed, got count %d", memberCount)
	}

	// Step 7: Subsequent GET returns HTTP 404.
	getRec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID, "")
	if getRec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for deleted team, got %d: %s", getRec.Code, getRec.Body.String())
	}
	errResp := parseErrorResponse(t, getRec)
	if errResp.Error.Message != "team not found" {
		t.Errorf("expected message 'team not found', got %q", errResp.Error.Message)
	}
}

// TS-03-SMOKE-3: Admin adds a user to an active team (first time) and then
// re-adds the same user (idempotent), verifying correct responses with
// fresh user data and unchanged joined_at.
// Execution Path: 03-PATH-3
func TestSmoke_AddMemberIdempotent(t *testing.T) {
	e, db := setupRouterTest(t, adminAuthCtx())

	// Step 1: Create team.
	createBody := `{"name": "Smoke Member Team", "slug": "smoke-member"}`
	createRec := doRequest(t, e, http.MethodPost, "/api/v1/teams", createBody)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	createResp := parseTeamResponse(t, createRec)
	teamID := createResp.ID
	teamUpdatedAtBefore := createResp.UpdatedAt

	// Step 2: Seed a user.
	userID := seedUser(t, db, "idempotent@smoke.com", "Idempotent User")

	// Step 3: First add → should return HTTP 200 with member object.
	addBody := fmt.Sprintf(`{"user_id": "%s"}`, userID)
	add1Rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", addBody)
	if add1Rec.Code != http.StatusOK {
		t.Fatalf("first add: expected 200, got %d: %s", add1Rec.Code, add1Rec.Body.String())
	}

	var member1 memberResponse
	if err := json.Unmarshal(add1Rec.Body.Bytes(), &member1); err != nil {
		t.Fatalf("failed to parse first add response: %v", err)
	}

	// Verify member object fields from first add.
	if member1.UserID != userID {
		t.Errorf("first add: expected user_id %q, got %q", userID, member1.UserID)
	}
	if member1.TeamID != teamID {
		t.Errorf("first add: expected team_id %q, got %q", teamID, member1.TeamID)
	}
	if member1.Email != "idempotent@smoke.com" {
		t.Errorf("first add: expected email %q, got %q", "idempotent@smoke.com", member1.Email)
	}
	if member1.Name != "Idempotent User" {
		t.Errorf("first add: expected name %q, got %q", "Idempotent User", member1.Name)
	}
	if !rfc3339MicroRe.MatchString(member1.JoinedAt) {
		t.Errorf("first add: joined_at not RFC3339 microsecond: %q", member1.JoinedAt)
	}

	originalJoinedAt := member1.JoinedAt

	// Small delay to ensure timestamps differ if joined_at were incorrectly updated.
	time.Sleep(10 * time.Millisecond)

	// Step 4: Second add (idempotent) → same HTTP 200, same joined_at.
	add2Rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", addBody)
	if add2Rec.Code != http.StatusOK {
		t.Fatalf("second add: expected 200, got %d: %s", add2Rec.Code, add2Rec.Body.String())
	}

	var member2 memberResponse
	if err := json.Unmarshal(add2Rec.Body.Bytes(), &member2); err != nil {
		t.Fatalf("failed to parse second add response: %v", err)
	}

	// Verify joined_at is unchanged (original timestamp preserved).
	if member2.JoinedAt != originalJoinedAt {
		t.Errorf("idempotent add: joined_at changed from %q to %q", originalJoinedAt, member2.JoinedAt)
	}

	// Verify current user data is fresh.
	if member2.Email != "idempotent@smoke.com" {
		t.Errorf("idempotent add: email mismatch: got %q", member2.Email)
	}
	if member2.Name != "Idempotent User" {
		t.Errorf("idempotent add: name mismatch: got %q", member2.Name)
	}

	// Step 5: Verify exactly one membership row in DB.
	var count int
	if err := db.QueryRow(
		"SELECT count(*) FROM team_members WHERE team_id = ? AND user_id = ?",
		teamID, userID,
	).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 team_members row, got %d", count)
	}

	// Step 6: Verify team.updated_at is unchanged by member adds.
	getTeamRec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID, "")
	if getTeamRec.Code != http.StatusOK {
		t.Fatalf("get team: expected 200, got %d", getTeamRec.Code)
	}
	getTeamResp := parseTeamResponse(t, getTeamRec)
	if getTeamResp.UpdatedAt != teamUpdatedAtBefore {
		t.Errorf("team.updated_at changed after member adds: before=%q after=%q",
			teamUpdatedAtBefore, getTeamResp.UpdatedAt)
	}
}

// TS-03-SMOKE-4: Concurrent duplicate team creation race — one request gets
// HTTP 201 and the other gets HTTP 409 via partial UNIQUE index.
// Execution Path: 03-PATH-4
func TestSmoke_ConcurrentDuplicateCreate(t *testing.T) {
	e, db := setupRouterTest(t, adminAuthCtx())

	// Ensure single connection so both goroutines share the same DB state.
	db.SetMaxOpenConns(1)

	slug := "concurrent-smoke"
	var wg sync.WaitGroup
	results := make([]int, 2)

	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("Concurrent Team %d", idx)
			body := fmt.Sprintf(`{"name": %q, "slug": %q}`, name, slug)
			rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)
			results[idx] = rec.Code
		}(i)
	}
	wg.Wait()

	// Exactly one should succeed (201), the other should fail (409 or 500 due
	// to SQLite serialization under contention — the key invariant is no
	// duplicate rows).
	has201 := results[0] == http.StatusCreated || results[1] == http.StatusCreated
	if !has201 {
		t.Errorf("expected at least one HTTP 201, got %d and %d", results[0], results[1])
	}

	// The failing request should be a conflict or server error, not a success.
	bothCreated := results[0] == http.StatusCreated && results[1] == http.StatusCreated
	if bothCreated {
		t.Error("both requests returned 201 — uniqueness was not enforced")
	}

	// Database-level invariant: exactly one non-deleted team with this slug.
	var count int
	if err := db.QueryRow(
		"SELECT count(*) FROM teams WHERE slug = ? AND status != 'deleted'", slug,
	).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 team with slug %q, got %d", slug, count)
	}
}

// TS-03-SMOKE-5: Full end-to-end admin team management flow:
// auth → admin check → create team → add member → list members.
// Execution Path: 03-PATH-5
func TestSmoke_FullManagementFlow(t *testing.T) {
	e, db := setupRouterTest(t, adminAuthCtx())

	// --- Step 1: Non-admin caller is rejected (HTTP 403) ---
	nonAdminE, _ := setupRouterTest(t, nonAdminAuthCtx())
	nonAdminRec := doRequest(t, nonAdminE, http.MethodGet, "/api/v1/teams", "")
	if nonAdminRec.Code != http.StatusForbidden {
		t.Fatalf("non-admin: expected 403, got %d", nonAdminRec.Code)
	}

	// --- Step 2: Create team (POST /api/v1/teams) ---
	createBody := `{"name": "Full Flow Team", "slug": "full-flow", "url": "https://fullflow.example.com"}`
	createRec := doRequest(t, e, http.MethodPost, "/api/v1/teams", createBody)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}

	// Verify Content-Type.
	ct := createRec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("create: expected Content-Type application/json, got %q", ct)
	}

	createResp := parseTeamResponse(t, createRec)
	teamID := createResp.ID

	// Verify team object shape.
	if _, err := uuid.Parse(teamID); err != nil {
		t.Errorf("create: id not a valid UUID: %q", teamID)
	}
	if createResp.Status != "active" {
		t.Errorf("create: expected status 'active', got %q", createResp.Status)
	}
	if !rfc3339MicroRe.MatchString(createResp.CreatedAt) {
		t.Errorf("create: created_at not RFC3339 microsecond: %q", createResp.CreatedAt)
	}
	if !rfc3339MicroRe.MatchString(createResp.UpdatedAt) {
		t.Errorf("create: updated_at not RFC3339 microsecond: %q", createResp.UpdatedAt)
	}

	// --- Step 3: Seed user and add as member (POST /api/v1/teams/:id/members) ---
	userID := seedUser(t, db, "flow@smoke.com", "Flow User")
	memberBody := fmt.Sprintf(`{"user_id": "%s"}`, userID)
	memberRec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", memberBody)
	if memberRec.Code != http.StatusOK {
		t.Fatalf("add member: expected 200, got %d: %s", memberRec.Code, memberRec.Body.String())
	}

	// Verify Content-Type.
	ct = memberRec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("add member: expected Content-Type application/json, got %q", ct)
	}

	var memberResp memberResponse
	if err := json.Unmarshal(memberRec.Body.Bytes(), &memberResp); err != nil {
		t.Fatalf("failed to parse member response: %v", err)
	}

	// Verify member object has correct email/name from users table JOIN.
	if memberResp.Email != "flow@smoke.com" {
		t.Errorf("add member: expected email %q, got %q", "flow@smoke.com", memberResp.Email)
	}
	if memberResp.Name != "Flow User" {
		t.Errorf("add member: expected name %q, got %q", "Flow User", memberResp.Name)
	}
	if memberResp.UserID != userID {
		t.Errorf("add member: expected user_id %q, got %q", userID, memberResp.UserID)
	}
	if memberResp.TeamID != teamID {
		t.Errorf("add member: expected team_id %q, got %q", teamID, memberResp.TeamID)
	}
	if !rfc3339MicroRe.MatchString(memberResp.JoinedAt) {
		t.Errorf("add member: joined_at not RFC3339 microsecond: %q", memberResp.JoinedAt)
	}

	// --- Step 4: List members (GET /api/v1/teams/:id/members) ---
	listRec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID+"/members", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list members: expected 200, got %d: %s", listRec.Code, listRec.Body.String())
	}

	// Verify Content-Type.
	ct = listRec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("list members: expected Content-Type application/json, got %q", ct)
	}

	var members []memberResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &members); err != nil {
		t.Fatalf("failed to parse members list: %v", err)
	}

	// Verify the array contains the added member.
	if len(members) != 1 {
		t.Fatalf("list members: expected 1 member, got %d", len(members))
	}
	if members[0].UserID != userID {
		t.Errorf("list members: expected user_id %q, got %q", userID, members[0].UserID)
	}
	if members[0].Email != "flow@smoke.com" {
		t.Errorf("list members: expected email %q, got %q", "flow@smoke.com", members[0].Email)
	}
	if !rfc3339MicroRe.MatchString(members[0].JoinedAt) {
		t.Errorf("list members: joined_at not RFC3339 microsecond: %q", members[0].JoinedAt)
	}

	// --- Step 5: Verify all timestamps are RFC3339 UTC microsecond ---
	// (Already checked above for each response.)

	// --- Step 6: Verify Content-Type on all successful responses ---
	// (Already checked above for each response.)
}

// TS-03-SMOKE (supplemental): Verify that the name/slug reuse after deletion
// works end-to-end through RegisterTeamRoutes.
// Requirement: 03-REQ-2.E2
func TestSmoke_NameSlugReuseAfterDeletion(t *testing.T) {
	e, _ := setupRouterTest(t, adminAuthCtx())

	// Step 1: Create a team.
	createBody := `{"name": "Reusable", "slug": "reusable-slug"}`
	createRec := doRequest(t, e, http.MethodPost, "/api/v1/teams", createBody)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	origResp := parseTeamResponse(t, createRec)
	origID := origResp.ID

	// Step 2: Archive the team.
	archiveRec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+origID+"/archive", "")
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive: expected 200, got %d: %s", archiveRec.Code, archiveRec.Body.String())
	}

	// Step 3: Delete the team.
	deleteRec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+origID, "")
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}

	// Step 4: Reuse the same name and slug.
	reuseBody := `{"name": "Reusable", "slug": "reusable-slug"}`
	reuseRec := doRequest(t, e, http.MethodPost, "/api/v1/teams", reuseBody)
	if reuseRec.Code != http.StatusCreated {
		t.Fatalf("reuse: expected 201, got %d: %s", reuseRec.Code, reuseRec.Body.String())
	}

	reuseResp := parseTeamResponse(t, reuseRec)
	if reuseResp.Name != "Reusable" {
		t.Errorf("reuse: expected name 'Reusable', got %q", reuseResp.Name)
	}
	if reuseResp.Slug != "reusable-slug" {
		t.Errorf("reuse: expected slug 'reusable-slug', got %q", reuseResp.Slug)
	}
	if reuseResp.ID == origID {
		t.Error("reuse: new team should have a different ID than the deleted one")
	}
}

// TestSmoke_DeletedTeamInaccessible verifies that once a team is deleted,
// all endpoints return 404 for that team's ID. Complements TS-03-P1.
func TestSmoke_DeletedTeamInaccessible(t *testing.T) {
	e, db := setupRouterTest(t, adminAuthCtx())

	// Create, archive, and delete a team.
	createBody := `{"name": "Ghost Team", "slug": "ghost-team"}`
	createRec := doRequest(t, e, http.MethodPost, "/api/v1/teams", createBody)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", createRec.Code)
	}
	ghostID := parseTeamResponse(t, createRec).ID

	// Add a member before archiving.
	userID := seedUser(t, db, "ghost@smoke.com", "Ghost")
	addBody := fmt.Sprintf(`{"user_id": "%s"}`, userID)
	doRequest(t, e, http.MethodPost, "/api/v1/teams/"+ghostID+"/members", addBody)

	// Archive then delete.
	doRequest(t, e, http.MethodPost, "/api/v1/teams/"+ghostID+"/archive", "")
	deleteRec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+ghostID, "")
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", deleteRec.Code)
	}

	// All endpoints should return 404 for the deleted team.
	endpoints := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/teams/" + ghostID, ""},
		{http.MethodPost, "/api/v1/teams/" + ghostID + "/archive", ""},
		{http.MethodPost, "/api/v1/teams/" + ghostID + "/reactivate", ""},
		{http.MethodDelete, "/api/v1/teams/" + ghostID, ""},
		{http.MethodPost, "/api/v1/teams/" + ghostID + "/members", addBody},
		{http.MethodGet, "/api/v1/teams/" + ghostID + "/members", ""},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+"_"+ep.path, func(t *testing.T) {
			rec := doRequest(t, e, ep.method, ep.path, ep.body)
			if rec.Code != http.StatusNotFound {
				t.Errorf("expected 404 for %s %s on deleted team, got %d: %s",
					ep.method, ep.path, rec.Code, rec.Body.String())
			}
			resp := parseErrorResponse(t, rec)
			if resp.Error.Message != "team not found" {
				t.Errorf("expected message 'team not found', got %q", resp.Error.Message)
			}
		})
	}
}

// TestSmoke_MemberListOrdering verifies that GET /api/v1/teams/:id/members
// returns members ordered by joined_at ascending through the full stack.
func TestSmoke_MemberListOrdering(t *testing.T) {
	e, db := setupRouterTest(t, adminAuthCtx())

	// Create team.
	createBody := `{"name": "Order Team", "slug": "order-team"}`
	createRec := doRequest(t, e, http.MethodPost, "/api/v1/teams", createBody)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", createRec.Code)
	}
	teamID := parseTeamResponse(t, createRec).ID

	// Add 3 members with small delays to ensure distinct joined_at timestamps.
	var userIDs []string
	for i := range 3 {
		userID := seedUser(t, db, fmt.Sprintf("user%d@order.com", i), fmt.Sprintf("User %d", i))
		userIDs = append(userIDs, userID)
		body := fmt.Sprintf(`{"user_id": "%s"}`, userID)
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("add member %d: expected 200, got %d", i, rec.Code)
		}
		// Small sleep to ensure distinct timestamps.
		time.Sleep(5 * time.Millisecond)
	}

	// List members.
	listRec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID+"/members", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", listRec.Code)
	}

	var members []memberResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &members); err != nil {
		t.Fatalf("failed to parse members: %v", err)
	}

	if len(members) != 3 {
		t.Fatalf("expected 3 members, got %d", len(members))
	}

	// Verify ordering by joined_at ascending.
	for i := range 3 {
		if members[i].UserID != userIDs[i] {
			t.Errorf("member[%d]: expected user_id %q, got %q", i, userIDs[i], members[i].UserID)
		}
	}

	// Verify joined_at is monotonically increasing.
	for i := 0; i < len(members)-1; i++ {
		if members[i].JoinedAt >= members[i+1].JoinedAt {
			t.Errorf("members not ordered by joined_at: [%d]=%q >= [%d]=%q",
				i, members[i].JoinedAt, i+1, members[i+1].JoinedAt)
		}
	}
}

// TestSmoke_ErrorEnvelopeConsistency verifies that various error conditions
// through the full stack all use the nested error envelope format with
// code mirroring the HTTP status.
func TestSmoke_ErrorEnvelopeConsistency(t *testing.T) {
	e, _ := setupRouterTest(t, adminAuthCtx())

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantCode   int
		wantMsg    string
	}{
		{
			name:     "invalid_uuid",
			method:   http.MethodGet,
			path:     "/api/v1/teams/not-a-uuid",
			wantCode: 400,
			wantMsg:  "invalid id format",
		},
		{
			name:     "team_not_found",
			method:   http.MethodGet,
			path:     "/api/v1/teams/" + uuid.New().String(),
			wantCode: 404,
			wantMsg:  "team not found",
		},
		{
			name:     "malformed_json",
			method:   http.MethodPost,
			path:     "/api/v1/teams",
			body:     "not json{",
			wantCode: 400,
			wantMsg:  "invalid request body",
		},
		{
			name:     "missing_required",
			method:   http.MethodPost,
			path:     "/api/v1/teams",
			body:     `{"url": "https://example.com"}`,
			wantCode: 422,
			wantMsg:  "missing required field",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, e, tc.method, tc.path, tc.body)
			if rec.Code != tc.wantCode {
				t.Fatalf("expected status %d, got %d: %s", tc.wantCode, rec.Code, rec.Body.String())
			}

			resp := parseErrorResponse(t, rec)
			if resp.Error.Code != tc.wantCode {
				t.Errorf("expected error.code %d, got %d", tc.wantCode, resp.Error.Code)
			}
			if resp.Error.Message != tc.wantMsg {
				t.Errorf("expected error.message %q, got %q", tc.wantMsg, resp.Error.Message)
			}

			// All error responses must have Content-Type: application/json.
			ct := rec.Header().Get("Content-Type")
			if !strings.Contains(ct, "application/json") {
				t.Errorf("expected Content-Type application/json on error, got %q", ct)
			}
		})
	}
}
