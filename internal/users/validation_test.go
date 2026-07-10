package users_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/users"
)

// ---------------------------------------------------------------------------
// TS-02-37: Username validation accepts alphanumeric+hyphen chars up to 39
// chars; rejects any invalid chars or length > 39.
// Requirement: 02-REQ-12.1
// ---------------------------------------------------------------------------

func TestValidation_UsernameRules(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)
	registry := newStubProviderRegistry("github")

	e := setupEcho()
	handler := users.CreateUserHandler(db, registry)
	e.POST("/api/v1/users", handler, setAuthContext(adminAuthContext()))

	tests := []struct {
		name     string
		username string
		wantCode int
	}{
		{
			name:     "valid_alphanumeric_hyphen",
			username: "alice-bob",
			wantCode: http.StatusCreated,
		},
		{
			name:     "valid_single_char",
			username: "a",
			wantCode: http.StatusCreated,
		},
		{
			name:     "valid_39_chars",
			username: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // exactly 39
			wantCode: http.StatusCreated,
		},
		{
			name:     "invalid_underscore",
			username: "alice_bob",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "invalid_space",
			username: "alice bob",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "invalid_40_chars",
			username: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 40 chars
			wantCode: http.StatusBadRequest,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each valid user needs a unique provider_id to avoid conflicts.
			providerID := "p-" + tt.username + "-" + strings.Repeat("x", i)
			body := `{"username":"` + tt.username + `","email":"e@e.com","provider":"github","provider_id":"` + providerID + `"}`

			req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("username %q: expected HTTP %d, got %d: %s",
					tt.username, tt.wantCode, rec.Code, rec.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-02-38: Username is stored as-is (case preserved) but uniqueness
// enforced by lowercased comparison.
// Requirement: 02-REQ-12.2
// ---------------------------------------------------------------------------

func TestValidation_CasePreservedUniqueness(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)
	registry := newStubProviderRegistry("github")

	e := setupEcho()
	handler := users.CreateUserHandler(db, registry)
	e.POST("/api/v1/users", handler, setAuthContext(adminAuthContext()))

	t.Run("create_Alice_preserves_case", func(t *testing.T) {
		body := `{"username":"Alice","email":"a@e.com","provider":"github","provider_id":"p10"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("expected HTTP 201, got %d: %s", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		// Username must be stored as-is (case preserved).
		if resp["username"] != "Alice" {
			t.Errorf("expected username 'Alice' (case preserved), got %v", resp["username"])
		}
	})

	t.Run("alice_conflicts_with_Alice", func(t *testing.T) {
		body := `{"username":"alice","email":"b@e.com","provider":"github","provider_id":"p11"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("expected HTTP 409 for case-insensitive duplicate, got %d: %s",
				rec.Code, rec.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// TS-02-39: updated_at is updated only when at least one field value changes;
// identical values on PUT do not bump updated_at.
// Requirement: 02-REQ-13.1
// ---------------------------------------------------------------------------

func TestValidation_UpdatedAtSemantics(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	t0 := "2025-01-01T00:00:00Z"
	insertTestUser(t, db, "user-uuid-10", "unchanged", "u@example.com", "Unchanged",
		"active", "github", "u-010", t0)

	e := setupEcho()
	e.PUT("/api/v1/users/:id", users.UpdateUserHandler(db), setAuthContext(userAuthContext("user-uuid-10")))

	t.Run("no_op_put_does_not_bump_updated_at", func(t *testing.T) {
		// Ensure time passes so any incorrect bump would be detectable.
		time.Sleep(10 * time.Millisecond)

		body := `{"full_name":"Unchanged"}`
		req := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-uuid-10", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		// Response updated_at should match the original.
		if resp["updated_at"] != t0 {
			t.Errorf("expected updated_at unchanged (%s), got %v", t0, resp["updated_at"])
		}

		// Verify in DB as well.
		var dbUpdatedAt string
		err := db.QueryRow("SELECT updated_at FROM users WHERE id = ?", "user-uuid-10").Scan(&dbUpdatedAt)
		if err != nil {
			t.Fatalf("failed to query updated_at: %v", err)
		}
		if dbUpdatedAt != t0 {
			t.Errorf("expected DB updated_at unchanged (%s), got %s", t0, dbUpdatedAt)
		}
	})

	t.Run("actual_change_bumps_updated_at", func(t *testing.T) {
		time.Sleep(10 * time.Millisecond)

		body := `{"full_name":"Changed"}`
		req := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-uuid-10", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		updatedAt, ok := resp["updated_at"].(string)
		if !ok {
			t.Fatal("expected updated_at to be a string")
		}
		if updatedAt <= t0 {
			t.Errorf("expected updated_at to be bumped (> %s), got %s", t0, updatedAt)
		}
	})
}

// ---------------------------------------------------------------------------
// TS-02-E28: Username with spaces, underscores, or special characters
// returns HTTP 400; user record not created or updated.
// Requirement: 02-REQ-12.E1
// ---------------------------------------------------------------------------

func TestValidation_EdgeCase_InvalidUsernameChars(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)
	registry := newStubProviderRegistry("github")

	e := setupEcho()
	handler := users.CreateUserHandler(db, registry)
	e.POST("/api/v1/users", handler, setAuthContext(adminAuthContext()))

	invalidUsernames := []struct {
		name     string
		username string
	}{
		{"underscore", "alice_bob"},
		{"space", "alice bob"},
		{"at_sign", "alice@bob"},
		{"dot", "alice.bob"},
		{"exclamation", "alice!"},
		{"hash", "alice#1"},
	}

	for _, tt := range invalidUsernames {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"username":"` + tt.username + `","email":"e@e.com","provider":"github","provider_id":"p-` + tt.name + `"}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("username %q: expected HTTP 400, got %d: %s",
					tt.username, rec.Code, rec.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-02-E29: Username exceeding 39 characters returns HTTP 400; user record
// not created.
// Requirement: 02-REQ-12.E2
// ---------------------------------------------------------------------------

func TestValidation_EdgeCase_UsernameTooLong(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)
	registry := newStubProviderRegistry("github")

	e := setupEcho()
	handler := users.CreateUserHandler(db, registry)
	e.POST("/api/v1/users", handler, setAuthContext(adminAuthContext()))

	// 40-character username (exceeds 39 limit).
	longUsername := strings.Repeat("a", 40)
	body := `{"username":"` + longUsername + `","email":"e@e.com","provider":"github","provider_id":"p1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected HTTP 400 for 40-char username, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify no user with long username in DB.
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM users WHERE LENGTH(username) > 39").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 users with username > 39 chars, got %d", count)
	}
}
