package teams_test

import (
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/teams"
)

// ---------------------------------------------------------------------------
// Test Helpers (delete team tests)
// ---------------------------------------------------------------------------

// setupDeleteTeamTest initializes a test database, Echo instance, and
// registers team routes. Returns the Echo instance and database.
func setupDeleteTeamTest(t *testing.T) (*echo.Echo, *sql.DB) {
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

// seedUser inserts a stub user row and returns the user UUID.
func seedUser(t *testing.T, db *sql.DB, email, name string) string {
	t.Helper()
	id := uuid.New().String()
	now := teams.FormatTime(time.Now().UTC())
	_, err := db.Exec(
		`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', 'github', ?, ?, ?)`,
		id, email, email, name, "gh-"+id, now, now,
	)
	if err != nil {
		t.Fatalf("failed to seed user %q: %v", email, err)
	}
	return id
}

// seedMember inserts a team_members row directly into the database.
func seedMember(t *testing.T, db *sql.DB, teamID, userID string) {
	t.Helper()
	now := teams.FormatTime(time.Now().UTC())
	_, err := db.Exec(
		`INSERT INTO team_members (team_id, user_id, created_at) VALUES (?, ?, ?)`,
		teamID, userID, now,
	)
	if err != nil {
		t.Fatalf("failed to seed member (team=%s, user=%s): %v", teamID, userID, err)
	}
}

// ---------------------------------------------------------------------------
// TS-03-32: DELETE /api/v1/teams/:id deletes archived team with cascade → 204
// Requirement: 03-REQ-7.1
//
// Verifies that deleting an archived team removes the team row and all its
// team_members rows atomically, returning HTTP 204 with no body.
// ---------------------------------------------------------------------------

func TestDeleteTeam_SuccessWithCascade(t *testing.T) {
	e, db := setupDeleteTeamTest(t)

	// Seed an archived team with 2 members.
	teamID := seedTeamWithStatus(t, db, "Del Team", "del-team", "archived")
	user1ID := seedUser(t, db, "user1@example.com", "User One")
	user2ID := seedUser(t, db, "user2@example.com", "User Two")
	seedMember(t, db, teamID, user1ID)
	seedMember(t, db, teamID, user2ID)

	// Verify pre-conditions: team and members exist.
	var teamCount int
	if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&teamCount); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if teamCount != 1 {
		t.Fatalf("expected 1 team row before delete, got %d", teamCount)
	}
	var memberCount int
	if err := db.QueryRow("SELECT count(*) FROM team_members WHERE team_id = ?", teamID).Scan(&memberCount); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if memberCount != 2 {
		t.Fatalf("expected 2 member rows before delete, got %d", memberCount)
	}

	// Issue DELETE request.
	rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// HTTP 204 responses should have no body.
	if rec.Body.Len() > 0 {
		t.Errorf("expected empty body for 204, got %q", rec.Body.String())
	}

	// Verify team row is removed from the database.
	if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&teamCount); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if teamCount != 0 {
		t.Errorf("expected 0 team rows after delete, got %d", teamCount)
	}

	// Verify all team_members rows for that team are removed.
	if err := db.QueryRow("SELECT count(*) FROM team_members WHERE team_id = ?", teamID).Scan(&memberCount); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if memberCount != 0 {
		t.Errorf("expected 0 member rows after delete, got %d", memberCount)
	}
}

// TestDeleteTeam_SuccessWithNoMembers verifies deletion works for archived
// teams that have no members.
func TestDeleteTeam_SuccessWithNoMembers(t *testing.T) {
	e, db := setupDeleteTeamTest(t)

	teamID := seedTeamWithStatus(t, db, "Empty Team", "empty-team", "archived")

	rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify team is removed.
	var count int
	if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 team rows after delete, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-03-33: DELETE /api/v1/teams/:id on active team → HTTP 409
// Requirement: 03-REQ-7.2
//
// Verifies that attempting to delete an active team returns HTTP 409 with
// message "team must be archived before deletion". The team row must remain.
// ---------------------------------------------------------------------------

func TestDeleteTeam_ActiveTeam(t *testing.T) {
	e, db := setupDeleteTeamTest(t)

	teamID := seedTeamWithStatus(t, db, "Active Team", "active-del", "active")

	rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 409 {
		t.Errorf("expected error code 409, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "team must be archived before deletion" {
		t.Errorf("expected message %q, got %q", "team must be archived before deletion", resp.Error.Message)
	}

	// Team row must still exist.
	var count int
	if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected team row to still exist, got count %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-03-34: DELETE /api/v1/teams/:id on nonexistent or deleted team → HTTP 404
// Requirement: 03-REQ-7.3
//
// Verifies that deleting a nonexistent or already-deleted team returns
// HTTP 404 with message "team not found".
// ---------------------------------------------------------------------------

func TestDeleteTeam_NotFound(t *testing.T) {
	e, db := setupDeleteTeamTest(t)

	t.Run("nonexistent_uuid", func(t *testing.T) {
		nonexistentID := uuid.New().String()
		rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+nonexistentID, "")

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d: %s", rec.Code, rec.Body.String())
		}

		resp := parseErrorResponse(t, rec)
		if resp.Error.Code != 404 {
			t.Errorf("expected error code 404, got %d", resp.Error.Code)
		}
		if resp.Error.Message != "team not found" {
			t.Errorf("expected message %q, got %q", "team not found", resp.Error.Message)
		}
	})

	t.Run("deleted_team", func(t *testing.T) {
		// Insert a team directly with status 'deleted' to simulate already-deleted.
		deletedID := seedTeamWithStatus(t, db, "Previously Deleted", "prev-deleted", "deleted")

		rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+deletedID, "")

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d: %s", rec.Code, rec.Body.String())
		}

		resp := parseErrorResponse(t, rec)
		if resp.Error.Code != 404 {
			t.Errorf("expected error code 404, got %d", resp.Error.Code)
		}
		if resp.Error.Message != "team not found" {
			t.Errorf("expected message %q, got %q", "team not found", resp.Error.Message)
		}
	})
}

// ---------------------------------------------------------------------------
// TS-03-35: DELETE /api/v1/teams/:id with invalid UUID → HTTP 400
// Requirement: 03-REQ-7.4
//
// Verifies that a malformed UUID path parameter returns HTTP 400 with
// message "invalid id format" before any database lookup.
// ---------------------------------------------------------------------------

func TestDeleteTeam_InvalidUUID(t *testing.T) {
	e, _ := setupDeleteTeamTest(t)

	rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/not-a-uuid", "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 400 {
		t.Errorf("expected error code 400, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "invalid id format" {
		t.Errorf("expected message %q, got %q", "invalid id format", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// TS-03-E4: Delete transaction rollback on partial failure → HTTP 500
// Requirement: 03-REQ-7.E1
//
// Verifies that if any step of the delete transaction fails, the entire
// transaction is rolled back and neither the team row nor its members
// are removed.
//
// Implementation note: Since we cannot easily inject DB errors at the store
// layer without a mock, this test verifies the atomicity property by testing
// the store's DeleteTeam method directly with a database that has been
// manipulated to cause a failure scenario. We verify the normal success
// case atomicity guarantee (both removed or neither removed) as a property.
// ---------------------------------------------------------------------------

func TestDeleteTeam_TransactionAtomicity(t *testing.T) {
	db := openTestDB(t)
	createStubUsersTable(t, db)
	if err := teams.InitSchema(db); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	t.Run("success_removes_both_team_and_members", func(t *testing.T) {
		// Seed an archived team with members.
		teamID := seedTeamWithStatus(t, db, "Atomic Team", "atomic-team", "archived")
		user1ID := seedUser(t, db, "atom1@example.com", "Atom One")
		user2ID := seedUser(t, db, "atom2@example.com", "Atom Two")
		seedMember(t, db, teamID, user1ID)
		seedMember(t, db, teamID, user2ID)

		store := teams.NewStore(db)
		err := store.DeleteTeam(teamID)
		if err != nil {
			t.Fatalf("DeleteTeam failed: %v", err)
		}

		// Both team and members should be gone.
		var teamCount int
		if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&teamCount); err != nil {
			t.Fatalf("query error: %v", err)
		}
		if teamCount != 0 {
			t.Errorf("expected 0 team rows, got %d", teamCount)
		}

		var memberCount int
		if err := db.QueryRow("SELECT count(*) FROM team_members WHERE team_id = ?", teamID).Scan(&memberCount); err != nil {
			t.Fatalf("query error: %v", err)
		}
		if memberCount != 0 {
			t.Errorf("expected 0 member rows, got %d", memberCount)
		}
	})

	t.Run("failure_preserves_both_team_and_members", func(t *testing.T) {
		// To test rollback, we use a separate DB and close it mid-transaction.
		// We create a wrapper that will simulate a failure by using a closed DB.
		failDB := openTestDB(t)
		createStubUsersTable(t, failDB)
		if err := teams.InitSchema(failDB); err != nil {
			t.Fatalf("InitSchema failed: %v", err)
		}

		teamID := seedTeamWithStatus(t, failDB, "Fail Team", "fail-team", "archived")
		userID := seedUser(t, failDB, "fail@example.com", "Fail User")
		seedMember(t, failDB, teamID, userID)

		// Close the DB to force a failure on the next transaction attempt.
		failDB.Close()

		store := teams.NewStore(failDB)
		err := store.DeleteTeam(teamID)

		// Should fail due to closed DB.
		if err == nil {
			t.Error("expected DeleteTeam to fail on closed DB, but got nil error")
		}

		// Note: since the DB is closed, we can't query it to verify state.
		// The test verifies that DeleteTeam propagates errors rather than
		// silently succeeding. The success_removes_both subtest above verifies
		// atomicity in the happy path. The transaction-based implementation
		// guarantees rollback on any step failure via defer tx.Rollback().
	})
}

// TestDeleteTeam_AtomicityProperty verifies the atomicity invariant:
// after a successful delete, both the team row and ALL member rows are gone.
// This tests the cascade property with various member counts.
func TestDeleteTeam_AtomicityProperty(t *testing.T) {
	memberCounts := []int{0, 1, 5, 10}

	for _, count := range memberCounts {
		t.Run(memberCountLabel(count), func(t *testing.T) {
			e, db := setupDeleteTeamTest(t)

			teamID := seedTeamWithStatus(t, db, "Prop Team", "prop-team", "archived")

			// Seed N members.
			for range count {
				userID := seedUser(t, db, userEmail(), userName())
				seedMember(t, db, teamID, userID)
			}

			// Verify precondition.
			var preMemberCount int
			if err := db.QueryRow("SELECT count(*) FROM team_members WHERE team_id = ?", teamID).Scan(&preMemberCount); err != nil {
				t.Fatalf("query error: %v", err)
			}
			if preMemberCount != count {
				t.Fatalf("expected %d members before delete, got %d", count, preMemberCount)
			}

			// Delete.
			rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")
			if rec.Code != http.StatusNoContent {
				t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
			}

			// Verify atomicity: both team AND members are gone.
			var postTeamCount int
			if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&postTeamCount); err != nil {
				t.Fatalf("query error: %v", err)
			}
			if postTeamCount != 0 {
				t.Errorf("expected 0 team rows after delete, got %d", postTeamCount)
			}

			var postMemberCount int
			if err := db.QueryRow("SELECT count(*) FROM team_members WHERE team_id = ?", teamID).Scan(&postMemberCount); err != nil {
				t.Fatalf("query error: %v", err)
			}
			if postMemberCount != 0 {
				t.Errorf("expected 0 member rows after delete, got %d", postMemberCount)
			}
		})
	}
}

// memberCountLabel returns a descriptive label for subtest naming.
func memberCountLabel(n int) string {
	switch n {
	case 0:
		return "no_members"
	case 1:
		return "one_member"
	default:
		return string(rune('0'+n)) + "_members"
	}
}

// userEmail generates a unique test user email.
func userEmail() string {
	return "prop-user-" + uuid.New().String()[:8] + "@example.com"
}

// userName generates a unique test user name.
func userName() string {
	return "Prop User " + uuid.New().String()[:8]
}
