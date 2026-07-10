package teams_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/teams"
)

// ---------------------------------------------------------------------------
// Test Helpers (property tests)
// ---------------------------------------------------------------------------

// setupPropertyTest initializes a test database, Echo instance, and registers
// team routes. Returns the Echo instance and database.
func setupPropertyTest(t *testing.T) (*echo.Echo, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	createStubUsersTable(t, db)
	if err := teams.InitSchema(db); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	store := teams.NewStore(db)
	handler := teams.NewHandler(store)

	e := echo.New()
	g := e.Group("/api/v1/teams")
	handler.RegisterRoutes(g)

	return e, db
}

// randomSlug generates a random slug that matches ^[a-z][a-z0-9-]{1,62}[a-z0-9]$.
func randomSlug(prefix string) string {
	suffix := uuid.New().String()[:8]
	slug := prefix + "-" + suffix
	// Ensure slug is at least 3 chars and starts with a letter.
	if len(slug) < 3 {
		slug = "a" + slug
	}
	// Replace any invalid characters (UUIDs are hex, so all lowercase).
	return strings.ToLower(slug)
}

// ===========================================================================
// 8.1: Property tests for deleted team inaccessibility and name/slug
//      uniqueness (PROP-1, PROP-2)
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-03-P1: For any physically deleted team, every endpoint returns HTTP 404
// Property: 03-PROP-1
// Validates: 03-REQ-4.2, 03-REQ-5.3, 03-REQ-6.3, 03-REQ-7.3, 03-REQ-8.4,
//            03-REQ-9.2, 03-REQ-11.3
//
// For each iteration, create a team, archive it, delete it, then verify
// every endpoint returns HTTP 404 with message "team not found".
// ---------------------------------------------------------------------------

func TestProperty_DeletedTeamInaccessible(t *testing.T) {
	e, db := setupPropertyTest(t)

	const iterations = 5

	for i := range iterations {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			slug := randomSlug(fmt.Sprintf("prop1-%d", i))
			name := fmt.Sprintf("Prop1 Team %d", i)

			// Create → archive → delete to get a fully deleted team.
			teamID := seedTeamWithStatus(t, db, name, slug, "active")

			// Archive.
			rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("archive: expected 200, got %d: %s", rec.Code, rec.Body.String())
			}

			// Delete.
			rec = doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")
			if rec.Code != http.StatusNoContent {
				t.Fatalf("delete: expected 204, got %d: %s", rec.Code, rec.Body.String())
			}

			// Verify the team is physically removed from the database.
			var count int
			if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&count); err != nil {
				t.Fatalf("query error: %v", err)
			}
			if count != 0 {
				t.Fatalf("expected team to be physically deleted, got count %d", count)
			}

			// Now verify every endpoint returns 404 for this deleted team.
			dummyUserID := uuid.New().String()
			endpoints := []struct {
				method string
				path   string
				body   string
			}{
				{http.MethodGet, "/api/v1/teams/" + teamID, ""},
				{http.MethodPost, "/api/v1/teams/" + teamID + "/archive", ""},
				{http.MethodPost, "/api/v1/teams/" + teamID + "/reactivate", ""},
				{http.MethodDelete, "/api/v1/teams/" + teamID, ""},
				{http.MethodPost, "/api/v1/teams/" + teamID + "/members", `{"user_id":"` + dummyUserID + `"}`},
				{http.MethodGet, "/api/v1/teams/" + teamID + "/members", ""},
			}

			for _, ep := range endpoints {
				label := ep.method + "_" + strings.ReplaceAll(ep.path, teamID, "ID")
				t.Run(label, func(t *testing.T) {
					rec := doRequest(t, e, ep.method, ep.path, ep.body)

					if rec.Code != http.StatusNotFound {
						t.Errorf("expected 404 for %s %s, got %d: %s",
							ep.method, ep.path, rec.Code, rec.Body.String())
						return
					}

					resp := parseErrorResponse(t, rec)
					if resp.Error.Message != "team not found" {
						t.Errorf("expected message %q, got %q", "team not found", resp.Error.Message)
					}

					// Verify no data is returned (only error envelope).
					var raw map[string]any
					if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
						t.Fatalf("failed to parse JSON: %v", err)
					}
					if _, hasID := raw["id"]; hasID {
						t.Error("response should not contain 'id' field for deleted team")
					}
				})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-03-P2: For any two non-deleted teams, they never share name or slug
// Property: 03-PROP-2
// Validates: 03-REQ-2.5, 03-REQ-2.6, 03-REQ-2.10, 03-REQ-2.E1, 03-REQ-2.E2
//
// Generate N random (name, slug) pairs and attempt to create N teams. Some
// may collide. After all creates, verify no two non-deleted teams share the
// same name or slug.
// ---------------------------------------------------------------------------

func TestProperty_NameSlugUniqueness(t *testing.T) {
	e, db := setupPropertyTest(t)

	// Generate a mix of unique and deliberately colliding names/slugs.
	type entry struct {
		name string
		slug string
	}

	entries := []entry{
		{"Unique Alpha", randomSlug("unique-alpha")},
		{"Unique Beta", randomSlug("unique-beta")},
		{"Unique Gamma", randomSlug("unique-gamma")},
		{"Unique Delta", randomSlug("unique-delta")},
		{"Unique Epsilon", randomSlug("unique-epsilon")},
		// Deliberately collide on name with first entry.
		{"Unique Alpha", randomSlug("unique-alpha-dup")},
		// Deliberately collide on slug with second entry.
		{"Unique Beta Dup", ""},
	}
	// Set the colliding slug to match the second entry's slug.
	entries[6].slug = entries[1].slug

	// Add more random entries.
	for i := range 5 {
		entries = append(entries, entry{
			name: fmt.Sprintf("Random Team P2 %d", i),
			slug: randomSlug(fmt.Sprintf("random-p2-%d", i)),
		})
	}

	for _, e2 := range entries {
		body := fmt.Sprintf(`{"name": %q, "slug": %q}`, e2.name, e2.slug)
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

		// We don't check individual results—some will be 201, some 409.
		// The invariant is checked after all creates.
		_ = rec
	}

	// Invariant check: no two non-deleted teams share name or slug.
	rows, err := db.Query(`SELECT name, slug FROM teams WHERE status != 'deleted'`)
	if err != nil {
		t.Fatalf("query error: %v", err)
	}
	defer rows.Close()

	var names []string
	var slugs []string
	for rows.Next() {
		var name, slug string
		if err := rows.Scan(&name, &slug); err != nil {
			t.Fatalf("scan error: %v", err)
		}
		names = append(names, name)
		slugs = append(slugs, slug)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	// Assert no duplicate names.
	nameSet := make(map[string]int)
	for _, n := range names {
		nameSet[n]++
	}
	for name, count := range nameSet {
		if count > 1 {
			t.Errorf("duplicate non-deleted team name %q found (count=%d)", name, count)
		}
	}

	// Assert no duplicate slugs.
	slugSet := make(map[string]int)
	for _, s := range slugs {
		slugSet[s]++
	}
	for slug, count := range slugSet {
		if count > 1 {
			t.Errorf("duplicate non-deleted team slug %q found (count=%d)", slug, count)
		}
	}
}

// TestProperty_NameSlugReuseAfterDelete verifies that uniqueness is scoped
// to non-deleted teams only — a deleted team's name/slug can be reused.
func TestProperty_NameSlugReuseAfterDelete(t *testing.T) {
	e, db := setupPropertyTest(t)

	const iterations = 3

	for i := range iterations {
		slug := randomSlug(fmt.Sprintf("reuse-%d", i))
		name := fmt.Sprintf("Reuse Team %d", i)

		// Create the team.
		body := fmt.Sprintf(`{"name": %q, "slug": %q}`, name, slug)
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("iteration %d create: expected 201, got %d: %s", i, rec.Code, rec.Body.String())
		}

		var createResp teamResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
			t.Fatalf("failed to parse create response: %v", err)
		}

		// Archive it.
		rec = doRequest(t, e, http.MethodPost, "/api/v1/teams/"+createResp.ID+"/archive", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("iteration %d archive: expected 200, got %d", i, rec.Code)
		}

		// Delete it.
		rec = doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+createResp.ID, "")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("iteration %d delete: expected 204, got %d", i, rec.Code)
		}

		// Reuse the same name and slug.
		rec = doRequest(t, e, http.MethodPost, "/api/v1/teams", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("iteration %d reuse: expected 201, got %d: %s", i, rec.Code, rec.Body.String())
		}

		var reuseResp teamResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &reuseResp); err != nil {
			t.Fatalf("failed to parse reuse response: %v", err)
		}

		// New team must have a different ID.
		if reuseResp.ID == createResp.ID {
			t.Errorf("reused team should have a new ID, but got same: %s", reuseResp.ID)
		}
		if reuseResp.Name != name {
			t.Errorf("expected reused team name %q, got %q", name, reuseResp.Name)
		}
		if reuseResp.Slug != slug {
			t.Errorf("expected reused team slug %q, got %q", slug, reuseResp.Slug)
		}

		// Clean up: archive and delete the reused team to avoid blocking next iteration.
		rec = doRequest(t, e, http.MethodPost, "/api/v1/teams/"+reuseResp.ID+"/archive", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("cleanup archive: expected 200, got %d", rec.Code)
		}
		rec = doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+reuseResp.ID, "")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("cleanup delete: expected 204, got %d", rec.Code)
		}
	}

	// Final invariant: no two non-deleted teams share name or slug.
	var nonDeletedCount int
	if err := db.QueryRow(`SELECT count(*) FROM teams WHERE status != 'deleted'`).Scan(&nonDeletedCount); err != nil {
		t.Fatalf("query error: %v", err)
	}

	rows, err := db.Query(`SELECT name, slug FROM teams WHERE status != 'deleted'`)
	if err != nil {
		t.Fatalf("query error: %v", err)
	}
	defer rows.Close()

	nameSet := make(map[string]int)
	slugSet := make(map[string]int)
	for rows.Next() {
		var name, slug string
		if err := rows.Scan(&name, &slug); err != nil {
			t.Fatalf("scan error: %v", err)
		}
		nameSet[name]++
		slugSet[slug]++
	}

	for name, count := range nameSet {
		if count > 1 {
			t.Errorf("duplicate non-deleted team name %q (count=%d)", name, count)
		}
	}
	for slug, count := range slugSet {
		if count > 1 {
			t.Errorf("duplicate non-deleted team slug %q (count=%d)", slug, count)
		}
	}
}

// ===========================================================================
// 8.2: Property tests for lifecycle ordering, cascade atomicity, and member
//      idempotency (PROP-3, PROP-4, PROP-5)
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-03-P3: Team lifecycle transitions follow the valid state machine
// Property: 03-PROP-3
// Validates: 03-REQ-11.1, 03-REQ-11.2, 03-REQ-11.3, 03-REQ-7.2
//
// Note: The actual lifecycle is bidirectional between active and archived
// (active ↔ archived), with a terminal transition from archived → deleted.
// The active → deleted direct transition is always rejected (HTTP 409).
// See: errata note on 03-PROP-3 (review finding).
// ---------------------------------------------------------------------------

func TestProperty_LifecycleTransitions(t *testing.T) {
	e, db := setupPropertyTest(t)

	// Use a deterministic seed for reproducibility in test output.
	rng := rand.New(rand.NewSource(42))

	const iterations = 10

	for i := range iterations {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			slug := randomSlug(fmt.Sprintf("lc-%d", i))
			name := fmt.Sprintf("Lifecycle Team %d", i)
			teamID := seedTeamWithStatus(t, db, name, slug, "active")

			state := "active"

			// Apply a random sequence of lifecycle operations (3-8 ops).
			numOps := 3 + rng.Intn(6)
			ops := []string{"archive", "reactivate", "delete"}

			for j := range numOps {
				op := ops[rng.Intn(len(ops))]

				switch op {
				case "archive":
					rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")
					if state == "active" {
						if rec.Code != http.StatusOK {
							t.Fatalf("op %d: archive active team: expected 200, got %d: %s",
								j, rec.Code, rec.Body.String())
						}
						state = "archived"
					} else if state == "archived" {
						if rec.Code != http.StatusConflict {
							t.Fatalf("op %d: archive archived team: expected 409, got %d: %s",
								j, rec.Code, rec.Body.String())
						}
						resp := parseErrorResponse(t, rec)
						if resp.Error.Message != "team is already archived" {
							t.Errorf("op %d: expected 'team is already archived', got %q", j, resp.Error.Message)
						}
					} else {
						// deleted state — expect 404
						if rec.Code != http.StatusNotFound {
							t.Fatalf("op %d: archive deleted team: expected 404, got %d: %s",
								j, rec.Code, rec.Body.String())
						}
					}

				case "reactivate":
					rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/reactivate", "")
					if state == "archived" {
						if rec.Code != http.StatusOK {
							t.Fatalf("op %d: reactivate archived team: expected 200, got %d: %s",
								j, rec.Code, rec.Body.String())
						}
						state = "active"
					} else if state == "active" {
						if rec.Code != http.StatusConflict {
							t.Fatalf("op %d: reactivate active team: expected 409, got %d: %s",
								j, rec.Code, rec.Body.String())
						}
						resp := parseErrorResponse(t, rec)
						if resp.Error.Message != "team is already active" {
							t.Errorf("op %d: expected 'team is already active', got %q", j, resp.Error.Message)
						}
					} else {
						if rec.Code != http.StatusNotFound {
							t.Fatalf("op %d: reactivate deleted team: expected 404, got %d: %s",
								j, rec.Code, rec.Body.String())
						}
					}

				case "delete":
					rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")
					if state == "active" {
						// active → deleted is always rejected.
						if rec.Code != http.StatusConflict {
							t.Fatalf("op %d: delete active team: expected 409, got %d: %s",
								j, rec.Code, rec.Body.String())
						}
						resp := parseErrorResponse(t, rec)
						if resp.Error.Message != "team must be archived before deletion" {
							t.Errorf("op %d: expected 'team must be archived before deletion', got %q",
								j, resp.Error.Message)
						}
					} else if state == "archived" {
						if rec.Code != http.StatusNoContent {
							t.Fatalf("op %d: delete archived team: expected 204, got %d: %s",
								j, rec.Code, rec.Body.String())
						}
						state = "deleted"
					} else {
						if rec.Code != http.StatusNotFound {
							t.Fatalf("op %d: delete deleted team: expected 404, got %d: %s",
								j, rec.Code, rec.Body.String())
						}
					}
				}

				// Terminal state — stop applying operations.
				if state == "deleted" {
					break
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-03-P4: Delete cascade is atomic
// Property: 03-PROP-4
// Validates: 03-REQ-7.1, 03-REQ-7.E1
//
// For archived teams with varying member counts (0 to 10), assert that
// deletion removes both the team row and all member rows, or neither.
// ---------------------------------------------------------------------------

func TestProperty_DeleteCascadeAtomic(t *testing.T) {
	e, db := setupPropertyTest(t)

	memberCounts := []int{0, 1, 2, 5, 10}

	for _, numMembers := range memberCounts {
		t.Run(fmt.Sprintf("members_%d", numMembers), func(t *testing.T) {
			slug := randomSlug(fmt.Sprintf("cascade-%d", numMembers))
			teamID := seedTeamWithStatus(t, db, fmt.Sprintf("Cascade Team %d", numMembers), slug, "archived")

			// Seed the specified number of members.
			for j := range numMembers {
				userID := seedUser(t, db, fmt.Sprintf("cascade-user-%d-%d@example.com", numMembers, j),
					fmt.Sprintf("Cascade User %d-%d", numMembers, j))
				seedMember(t, db, teamID, userID)
			}

			// Verify pre-conditions.
			var teamCountBefore int
			if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&teamCountBefore); err != nil {
				t.Fatalf("query error: %v", err)
			}
			if teamCountBefore != 1 {
				t.Fatalf("expected 1 team row before delete, got %d", teamCountBefore)
			}
			var memberCountBefore int
			if err := db.QueryRow("SELECT count(*) FROM team_members WHERE team_id = ?", teamID).Scan(&memberCountBefore); err != nil {
				t.Fatalf("query error: %v", err)
			}
			if memberCountBefore != numMembers {
				t.Fatalf("expected %d member rows before delete, got %d", numMembers, memberCountBefore)
			}

			// Perform the delete.
			rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")

			if rec.Code == http.StatusNoContent {
				// Success: both team and members must be removed.
				var teamCountAfter int
				if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&teamCountAfter); err != nil {
					t.Fatalf("query error: %v", err)
				}
				if teamCountAfter != 0 {
					t.Errorf("expected 0 team rows after delete, got %d", teamCountAfter)
				}

				var memberCountAfter int
				if err := db.QueryRow("SELECT count(*) FROM team_members WHERE team_id = ?", teamID).Scan(&memberCountAfter); err != nil {
					t.Fatalf("query error: %v", err)
				}
				if memberCountAfter != 0 {
					t.Errorf("expected 0 member rows after delete, got %d", memberCountAfter)
				}
			} else {
				// Failure: both team and members must still exist.
				var teamCountAfter int
				if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&teamCountAfter); err != nil {
					t.Fatalf("query error: %v", err)
				}
				if teamCountAfter != 1 {
					t.Errorf("expected team row preserved on failed delete, got count %d", teamCountAfter)
				}

				var memberCountAfter int
				if err := db.QueryRow("SELECT count(*) FROM team_members WHERE team_id = ?", teamID).Scan(&memberCountAfter); err != nil {
					t.Fatalf("query error: %v", err)
				}
				if memberCountAfter != numMembers {
					t.Errorf("expected %d member rows preserved on failed delete, got %d", numMembers, memberCountAfter)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-03-P5: Member add is idempotent with fresh user data
// Property: 03-PROP-5
// Validates: 03-REQ-8.2, 03-REQ-8.E1
//
// For each test, issue N repeated add-member requests for the same
// (team_id, user_id) pair. Assert exactly one DB row exists, and
// joined_at equals the original created_at.
// ---------------------------------------------------------------------------

func TestProperty_MemberAddIdempotent(t *testing.T) {
	e, db := setupPropertyTest(t)

	repeatCounts := []int{1, 2, 3, 5, 10}

	for _, n := range repeatCounts {
		t.Run(fmt.Sprintf("repeats_%d", n), func(t *testing.T) {
			slug := randomSlug(fmt.Sprintf("idemp-%d", n))
			teamID := seedTeamWithStatus(t, db, fmt.Sprintf("Idempotent Team %d", n), slug, "active")
			userID := seedUser(t, db, fmt.Sprintf("idemp-%d@example.com", n), fmt.Sprintf("Idemp User %d", n))

			body := fmt.Sprintf(`{"user_id": "%s"}`, userID)

			var originalJoinedAt string

			for j := range n {
				rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)

				if rec.Code != http.StatusOK {
					t.Fatalf("repeat %d: expected 200, got %d: %s", j, rec.Code, rec.Body.String())
				}

				var resp memberResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("repeat %d: failed to parse response: %v", j, err)
				}

				if j == 0 {
					originalJoinedAt = resp.JoinedAt
				} else {
					// joined_at must always equal the original value.
					if resp.JoinedAt != originalJoinedAt {
						t.Errorf("repeat %d: expected joined_at %q (original), got %q",
							j, originalJoinedAt, resp.JoinedAt)
					}
				}

				// Verify user data is always present.
				if resp.Email == "" {
					t.Errorf("repeat %d: expected non-empty email", j)
				}
				if resp.Name == "" {
					t.Errorf("repeat %d: expected non-empty name", j)
				}
			}

			// Invariant: exactly one row in team_members.
			var rowCount int
			if err := db.QueryRow(
				"SELECT count(*) FROM team_members WHERE team_id = ? AND user_id = ?",
				teamID, userID,
			).Scan(&rowCount); err != nil {
				t.Fatalf("query error: %v", err)
			}
			if rowCount != 1 {
				t.Errorf("expected exactly 1 team_members row, got %d", rowCount)
			}

			// Verify joined_at in DB matches original.
			var dbCreatedAt string
			if err := db.QueryRow(
				"SELECT created_at FROM team_members WHERE team_id = ? AND user_id = ?",
				teamID, userID,
			).Scan(&dbCreatedAt); err != nil {
				t.Fatalf("query error: %v", err)
			}
			// The DB value and the API response may differ in trailing zeros,
			// so parse both and compare.
			dbTime, err := time.Parse("2006-01-02T15:04:05.000000Z", dbCreatedAt)
			if err != nil {
				dbTime, err = time.Parse(time.RFC3339Nano, dbCreatedAt)
				if err != nil {
					t.Fatalf("failed to parse DB created_at %q: %v", dbCreatedAt, err)
				}
			}
			apiTime, err := time.Parse("2006-01-02T15:04:05.000000Z", originalJoinedAt)
			if err != nil {
				t.Fatalf("failed to parse API joined_at %q: %v", originalJoinedAt, err)
			}
			if !dbTime.Equal(apiTime) {
				t.Errorf("DB created_at (%s) != API joined_at (%s)", dbCreatedAt, originalJoinedAt)
			}
		})
	}
}

// ===========================================================================
// 8.3: Property tests for list ordering and updated_at semantics
//      (PROP-6, PROP-7)
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-03-P6: List ordering is stable and complete
// Property: 03-PROP-6
// Validates: 03-REQ-3.1, 03-REQ-3.2, 03-REQ-3.3, 03-REQ-3.4,
//            03-REQ-9.1, 03-REQ-9.4
//
// Generate random sets of teams with varying statuses. Assert:
// - list responses are ordered by created_at ascending
// - contain all matching records with no pagination omissions
// - never include deleted teams
// ---------------------------------------------------------------------------

func TestProperty_ListOrderingAndCompleteness(t *testing.T) {
	e, db := setupPropertyTest(t)

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Seed teams with varying statuses.
	type seededTeam struct {
		id     string
		status string
		name   string
	}
	var allTeams []seededTeam

	statuses := []string{"active", "archived", "deleted", "active", "active", "archived", "active", "deleted"}
	for i, status := range statuses {
		slug := randomSlug(fmt.Sprintf("order-%d", i))
		name := fmt.Sprintf("Order Team %d", i)
		createdAt := baseTime.Add(time.Duration(i) * time.Hour)
		id := seedTeamWithTime(t, db, name, slug, status, createdAt)
		allTeams = append(allTeams, seededTeam{id: id, status: status, name: name})
	}

	// Count expected results.
	var expectedActive, expectedActiveAndArchived int
	for _, st := range allTeams {
		if st.status == "active" {
			expectedActive++
			expectedActiveAndArchived++
		} else if st.status == "archived" {
			expectedActiveAndArchived++
		}
	}

	// Test 1: GET /api/v1/teams (active only).
	t.Run("active_only", func(t *testing.T) {
		rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		teamsList := parseTeamListResponse(t, rec.Body.Bytes())

		if len(teamsList) != expectedActive {
			t.Fatalf("expected %d active teams, got %d", expectedActive, len(teamsList))
		}

		// No deleted teams in response.
		for _, tm := range teamsList {
			if tm.Status == "deleted" {
				t.Errorf("deleted team %q should not appear in list", tm.Name)
			}
			if tm.Status != "active" {
				t.Errorf("expected only active teams, got status %q for %q", tm.Status, tm.Name)
			}
		}

		// Verify ordering by created_at ascending.
		for i := 0; i < len(teamsList)-1; i++ {
			t1, err1 := time.Parse("2006-01-02T15:04:05.000000Z", teamsList[i].CreatedAt)
			t2, err2 := time.Parse("2006-01-02T15:04:05.000000Z", teamsList[i+1].CreatedAt)
			if err1 != nil || err2 != nil {
				continue
			}
			if t1.After(t2) {
				t.Errorf("teams not ordered at %d: %s > %s",
					i, teamsList[i].CreatedAt, teamsList[i+1].CreatedAt)
			}
		}
	})

	// Test 2: GET /api/v1/teams?include_archived=true (active + archived).
	t.Run("include_archived", func(t *testing.T) {
		rec := doRequest(t, e, http.MethodGet, "/api/v1/teams?include_archived=true", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		teamsList := parseTeamListResponse(t, rec.Body.Bytes())

		if len(teamsList) != expectedActiveAndArchived {
			t.Fatalf("expected %d active+archived teams, got %d", expectedActiveAndArchived, len(teamsList))
		}

		// No deleted teams in response.
		for _, tm := range teamsList {
			if tm.Status == "deleted" {
				t.Errorf("deleted team %q should not appear in list", tm.Name)
			}
		}

		// Verify ordering by created_at ascending.
		for i := 0; i < len(teamsList)-1; i++ {
			t1, err1 := time.Parse("2006-01-02T15:04:05.000000Z", teamsList[i].CreatedAt)
			t2, err2 := time.Parse("2006-01-02T15:04:05.000000Z", teamsList[i+1].CreatedAt)
			if err1 != nil || err2 != nil {
				continue
			}
			if t1.After(t2) {
				t.Errorf("teams not ordered at %d: %s > %s",
					i, teamsList[i].CreatedAt, teamsList[i+1].CreatedAt)
			}
		}

		// Verify completeness: compare response IDs with expected IDs.
		responseIDs := make(map[string]bool)
		for _, tm := range teamsList {
			responseIDs[tm.ID] = true
		}
		for _, st := range allTeams {
			if st.status == "active" || st.status == "archived" {
				if !responseIDs[st.id] {
					t.Errorf("non-deleted team %q (id=%s, status=%s) missing from response",
						st.name, st.id, st.status)
				}
			} else if st.status == "deleted" {
				if responseIDs[st.id] {
					t.Errorf("deleted team %q (id=%s) should NOT appear in response", st.name, st.id)
				}
			}
		}
	})

	// Test 3: Member list ordering for a team with members.
	t.Run("member_list_ordering", func(t *testing.T) {
		teamID := seedTeamWithStatus(t, db, "Member Order Team", randomSlug("member-order"), "active")

		userIDs := make([]string, 8)
		for j := range 8 {
			userIDs[j] = seedUser(t, db,
				fmt.Sprintf("order-user-%d@example.com", j),
				fmt.Sprintf("Order User %d", j))
			seedMemberWithTime(t, db, teamID, userIDs[j], baseTime.Add(time.Duration(j)*time.Minute))
		}

		rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID+"/members", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		members := parseMemberListResponse(t, rec)
		if len(members) != 8 {
			t.Fatalf("expected 8 members, got %d", len(members))
		}

		// Verify ordering by joined_at ascending.
		for i := 0; i < len(members)-1; i++ {
			t1, err1 := time.Parse("2006-01-02T15:04:05.000000Z", members[i].JoinedAt)
			t2, err2 := time.Parse("2006-01-02T15:04:05.000000Z", members[i+1].JoinedAt)
			if err1 != nil || err2 != nil {
				continue
			}
			if t1.After(t2) {
				t.Errorf("members not ordered at %d: %s > %s",
					i, members[i].JoinedAt, members[i+1].JoinedAt)
			}
		}

		// Verify completeness: all seeded user IDs appear.
		memberUserIDs := make(map[string]bool)
		for _, m := range members {
			memberUserIDs[m.UserID] = true
		}
		for _, uid := range userIDs {
			if !memberUserIDs[uid] {
				t.Errorf("seeded user %s missing from member list", uid)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// TS-03-P7: updated_at reflects every team mutation
// Property: 03-PROP-7
// Validates: 03-REQ-11.4, 03-REQ-5.1, 03-REQ-6.1
//
// Apply random sequences of archive/reactivate and member-add operations.
// Assert:
// - updated_at is refreshed on lifecycle transitions (archive, reactivate)
// - updated_at is unchanged on member adds
// - updated_at is not observable after deletion
// ---------------------------------------------------------------------------

func TestProperty_UpdatedAtSemantics(t *testing.T) {
	e, db := setupPropertyTest(t)

	rng := rand.New(rand.NewSource(99))

	const iterations = 5

	for i := range iterations {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			slug := randomSlug(fmt.Sprintf("upd-%d", i))
			name := fmt.Sprintf("Updated Team %d", i)

			// Seed with deliberately old timestamps.
			oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			teamID := seedTeamWithStatusAndTimes(t, db, name, slug, "active", oldTime, oldTime)

			// Seed a user for member-add operations.
			userID := seedUser(t, db, fmt.Sprintf("upd-user-%d@example.com", i), fmt.Sprintf("Upd User %d", i))

			state := "active"
			var lastUpdatedAt time.Time
			// Read initial updated_at.
			var updStr string
			if err := db.QueryRow("SELECT updated_at FROM teams WHERE id = ?", teamID).Scan(&updStr); err != nil {
				t.Fatalf("query error: %v", err)
			}
			var parseErr error
			lastUpdatedAt, parseErr = time.Parse("2006-01-02T15:04:05.000000Z", updStr)
			if parseErr != nil {
				lastUpdatedAt, parseErr = time.Parse(time.RFC3339Nano, updStr)
				if parseErr != nil {
					t.Fatalf("failed to parse initial updated_at %q: %v", updStr, parseErr)
				}
			}

			// Apply a random sequence of operations.
			numOps := 3 + rng.Intn(5)
			ops := []string{"archive", "reactivate", "add_member"}
			memberAdded := false

			for j := range numOps {
				if state == "deleted" {
					break
				}

				op := ops[rng.Intn(len(ops))]

				switch op {
				case "archive":
					if state != "active" {
						continue // skip invalid transition
					}
					rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")
					if rec.Code != http.StatusOK {
						t.Fatalf("op %d archive: expected 200, got %d: %s", j, rec.Code, rec.Body.String())
					}
					resp := parseTeamResponse(t, rec)
					newUpdatedAt, err := time.Parse("2006-01-02T15:04:05.000000Z", resp.UpdatedAt)
					if err != nil {
						t.Fatalf("op %d: failed to parse updated_at: %v", j, err)
					}
					if newUpdatedAt.Before(lastUpdatedAt) {
						t.Errorf("op %d archive: updated_at went backward: %s < %s",
							j, resp.UpdatedAt, teams.FormatTime(lastUpdatedAt))
					}
					lastUpdatedAt = newUpdatedAt
					state = "archived"

				case "reactivate":
					if state != "archived" {
						continue // skip invalid transition
					}
					rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/reactivate", "")
					if rec.Code != http.StatusOK {
						t.Fatalf("op %d reactivate: expected 200, got %d: %s", j, rec.Code, rec.Body.String())
					}
					resp := parseTeamResponse(t, rec)
					newUpdatedAt, err := time.Parse("2006-01-02T15:04:05.000000Z", resp.UpdatedAt)
					if err != nil {
						t.Fatalf("op %d: failed to parse updated_at: %v", j, err)
					}
					if newUpdatedAt.Before(lastUpdatedAt) {
						t.Errorf("op %d reactivate: updated_at went backward: %s < %s",
							j, resp.UpdatedAt, teams.FormatTime(lastUpdatedAt))
					}
					lastUpdatedAt = newUpdatedAt
					state = "active"

				case "add_member":
					if state != "active" {
						continue // can only add members to active teams
					}
					// Record updated_at before member add.
					var beforeUpd string
					if err := db.QueryRow("SELECT updated_at FROM teams WHERE id = ?", teamID).Scan(&beforeUpd); err != nil {
						t.Fatalf("op %d: query error: %v", j, err)
					}

					if !memberAdded {
						body := fmt.Sprintf(`{"user_id": "%s"}`, userID)
						rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)
						if rec.Code != http.StatusOK {
							t.Fatalf("op %d add_member: expected 200, got %d: %s",
								j, rec.Code, rec.Body.String())
						}
						memberAdded = true
					} else {
						// Create a new user for additional member adds.
						extraUserID := seedUser(t, db,
							fmt.Sprintf("extra-%d-%d@example.com", i, j),
							fmt.Sprintf("Extra User %d-%d", i, j))
						body := fmt.Sprintf(`{"user_id": "%s"}`, extraUserID)
						rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)
						if rec.Code != http.StatusOK {
							t.Fatalf("op %d add_member: expected 200, got %d: %s",
								j, rec.Code, rec.Body.String())
						}
					}

					// Verify updated_at is unchanged after member add.
					var afterUpd string
					if err := db.QueryRow("SELECT updated_at FROM teams WHERE id = ?", teamID).Scan(&afterUpd); err != nil {
						t.Fatalf("op %d: query error: %v", j, err)
					}
					if beforeUpd != afterUpd {
						t.Errorf("op %d add_member: updated_at changed (before=%q, after=%q) — should be unchanged",
							j, beforeUpd, afterUpd)
					}
				}
			}

			// If team is archived, delete it and verify updated_at is not observable.
			if state == "archived" {
				rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")
				if rec.Code != http.StatusNoContent {
					t.Fatalf("final delete: expected 204, got %d: %s", rec.Code, rec.Body.String())
				}

				var count int
				if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&count); err != nil {
					t.Fatalf("query error: %v", err)
				}
				if count != 0 {
					t.Errorf("expected team to be deleted, got count %d", count)
				}
			} else if state == "active" {
				// Archive then delete to verify updated_at is not observable after deletion.
				rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")
				if rec.Code != http.StatusOK {
					t.Fatalf("final archive: expected 200, got %d", rec.Code)
				}
				rec = doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")
				if rec.Code != http.StatusNoContent {
					t.Fatalf("final delete: expected 204, got %d", rec.Code)
				}

				var count int
				if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&count); err != nil {
					t.Fatalf("query error: %v", err)
				}
				if count != 0 {
					t.Errorf("expected team to be deleted, got count %d", count)
				}
			}
		})
	}
}
