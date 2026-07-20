package workspace

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TS-01-P1: For any set of workspace create requests with the same slug,
// only one can succeed; the slug is unique across all rows at any point in
// time.
// Property: 01-PROP-1
// Validates: 01-REQ-1.1, 01-REQ-5.2
func TestPropWorkspace_SlugUniqueness(t *testing.T) {
	env := newTestEnv(t)

	const slug = "contested-slug"
	const N = 5

	type result struct {
		code int
	}

	// Generate N concurrent create requests with the same slug from distinct users.
	var wg sync.WaitGroup
	results := make([]result, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			userID := fmt.Sprintf("user-%d", idx)
			gitURL := fmt.Sprintf("https://github.com/org/repo-%d", idx)
			body := fmt.Sprintf(`{"slug":%q,"git_url":%q}`, slug, gitURL)
			auth := userAuth(userID)
			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
			results[idx] = result{code: rec.Code}
		}(i)
	}
	wg.Wait()

	// Assert exactly one 201 and the rest 409.
	created := 0
	conflicted := 0
	for i, r := range results {
		switch r.code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicted++
		default:
			t.Errorf("request %d: unexpected status %d; want 201 or 409", i, r.code)
		}
	}
	if created != 1 {
		t.Errorf("expected exactly 1 created (201); got %d", created)
	}
	if conflicted != N-1 {
		t.Errorf("expected %d conflicts (409); got %d", N-1, conflicted)
	}

	// Database invariant: exactly one row with this slug.
	var count int
	if err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", slug).Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("DB contains %d rows with slug %q; want exactly 1", count, slug)
	}
}

// TS-01-P2: For any workspace record in the workspaces table, the owner_id
// is always non-null and references a real user identity, never an admin-token
// identity.
// Property: 01-PROP-2
// Validates: 01-REQ-4.2, 01-REQ-5.E1
func TestPropWorkspace_OwnerIsRealUser(t *testing.T) {
	env := newTestEnv(t)

	// Create workspaces via user API keys.
	users := []string{"user-a", "user-b", "user-c"}
	for i, uid := range users {
		body := fmt.Sprintf(`{"slug":"ws-%d","git_url":"https://github.com/org/repo-%d"}`, i, i)
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, userAuth(uid))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create ws-%d: status = %d; want %d", i, rec.Code, http.StatusCreated)
		}
	}

	// Attempt create with admin token — must be rejected.
	adminBody := `{"slug":"admin-ws","git_url":"https://github.com/org/admin-repo"}`
	adminRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", adminBody, adminAuth())
	if adminRec.Code != http.StatusForbidden {
		t.Errorf("admin create: status = %d; want %d", adminRec.Code, http.StatusForbidden)
	}

	// Invariant: all rows have non-null, non-empty owner_id.
	rows, err := env.db.Query("SELECT slug, owner_id FROM workspaces")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var slug, ownerID string
		if err := rows.Scan(&slug, &ownerID); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		if ownerID == "" {
			t.Errorf("workspace %q has empty owner_id; want non-empty user ID", slug)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	// No row should have been created by the admin attempt.
	var count int
	if err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "admin-ws").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("admin-created workspace exists; want 0 rows")
	}
}

// TS-01-P3: For any workspace record in the workspaces table, the status
// column contains exactly 'active' or 'archived'; the value 'deleted' is
// never present.
// Property: 01-PROP-3
// Validates: 01-REQ-1.9, 01-REQ-10.5
func TestPropWorkspace_ValidStatusValues(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("alice-id")

	// Create.
	body := `{"slug":"lifecycle-ws","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d; want %d", rec.Code, http.StatusCreated)
	}
	assertNoDeletedStatus(t, env)

	// Archive.
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/lifecycle-ws/archive", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive: status = %d; want %d", rec.Code, http.StatusOK)
	}
	assertNoDeletedStatus(t, env)

	// Reactivate.
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/lifecycle-ws/reactivate", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivate: status = %d; want %d", rec.Code, http.StatusOK)
	}
	assertNoDeletedStatus(t, env)

	// Archive again then delete.
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/lifecycle-ws/archive", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive(2): status = %d; want %d", rec.Code, http.StatusOK)
	}
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/lifecycle-ws", "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d; want %d", rec.Code, http.StatusNoContent)
	}
	assertNoDeletedStatus(t, env)
}

// assertNoDeletedStatus checks that no row in the workspaces table has a
// status value outside {'active', 'archived'}.
func assertNoDeletedStatus(t *testing.T, env *testEnv) {
	t.Helper()
	var count int
	err := env.db.QueryRow(
		`SELECT COUNT(*) FROM workspaces WHERE status NOT IN ('active', 'archived')`,
	).Scan(&count)
	if err != nil {
		t.Fatalf("status invariant query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("found %d rows with invalid status (not 'active' or 'archived'); want 0", count)
	}
}

// TS-01-P4: For any DELETE /api/v1/workspaces/:slug request, a workspace row
// is physically removed only when its status was 'archived' at the time of
// deletion; active workspace delete is always rejected.
// Property: 01-PROP-4
// Validates: 01-REQ-10.1, 01-REQ-10.2
func TestPropWorkspace_DeleteOnlyFromArchived(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	t.Run("active workspace cannot be deleted", func(t *testing.T) {
		env.seedWorkspace(t, &Workspace{
			Slug:    "active-ws",
			GitURL:  "https://github.com/org/repo-active",
			OwnerID: "alice-id",
			Status:  "active",
		})

		rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/active-ws", "", auth)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE active: status = %d; want %d", rec.Code, http.StatusBadRequest)
		}

		// Row must still be present.
		var count int
		if err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "active-ws").Scan(&count); err != nil {
			t.Fatalf("count query failed: %v", err)
		}
		if count != 1 {
			t.Errorf("active-ws row count = %d; want 1 (not deleted)", count)
		}
	})

	t.Run("archived workspace can be deleted", func(t *testing.T) {
		env.seedWorkspace(t, &Workspace{
			Slug:    "archived-ws",
			GitURL:  "https://github.com/org/repo-archived",
			OwnerID: "alice-id",
			Status:  "archived",
		})

		rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/archived-ws", "", auth)
		if rec.Code != http.StatusNoContent {
			t.Errorf("DELETE archived: status = %d; want %d", rec.Code, http.StatusNoContent)
		}

		// Row must be absent.
		var count int
		if err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "archived-ws").Scan(&count); err != nil {
			t.Fatalf("count query failed: %v", err)
		}
		if count != 0 {
			t.Errorf("archived-ws row count = %d; want 0 (deleted)", count)
		}
	})
}

// TS-01-P5: For any GET /api/v1/workspaces response for a non-admin
// credential, all returned workspaces have owner_id equal to the
// authenticated user's ID.
// Property: 01-PROP-5
// Validates: 01-REQ-6.2, 01-REQ-4.6
func TestPropWorkspace_AccessScoping(t *testing.T) {
	env := newTestEnv(t)

	// Create workspaces for three users.
	users := []struct {
		id   string
		slug string
	}{
		{"user-a", "ws-a"},
		{"user-b", "ws-b"},
		{"user-c", "ws-c"},
	}
	for _, u := range users {
		env.seedWorkspace(t, &Workspace{
			Slug:    u.slug,
			GitURL:  fmt.Sprintf("https://github.com/org/%s", u.slug),
			OwnerID: u.id,
			Status:  "active",
		})
	}

	// Each user's listing must contain only their own workspaces.
	for _, u := range users {
		t.Run("user_"+u.id, func(t *testing.T) {
			rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", userAuth(u.id))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
			}

			workspaces := parseWorkspaceListJSON(t, rec)
			for _, ws := range workspaces {
				if ws.OwnerID != u.id {
					t.Errorf("user %s: workspace %q has owner_id %q; want %q",
						u.id, ws.Slug, ws.OwnerID, u.id)
				}
			}
		})
	}

	// Also test PAT scoping.
	t.Run("PAT scoping", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "",
			patAuth("user-a", "workspaces:read"))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
		}

		workspaces := parseWorkspaceListJSON(t, rec)
		for _, ws := range workspaces {
			if ws.OwnerID != "user-a" {
				t.Errorf("PAT user-a: workspace %q has owner_id %q; want %q",
					ws.Slug, ws.OwnerID, "user-a")
			}
		}
	})
}

// TS-01-P6: For any GET /api/v1/workspaces response when include_archived is
// absent or false, no archived workspace appears in the response array.
// Property: 01-PROP-6
// Validates: 01-REQ-6.1, 01-REQ-6.2
func TestPropWorkspace_ArchivedExcludedByDefault(t *testing.T) {
	env := newTestEnv(t)

	// Seed a mix of active and archived workspaces.
	env.seedWorkspace(t, &Workspace{
		Slug:    "active-one",
		GitURL:  "https://github.com/org/repo1",
		OwnerID: "alice-id",
		Status:  "active",
	})
	env.seedWorkspace(t, &Workspace{
		Slug:    "archived-one",
		GitURL:  "https://github.com/org/repo2",
		OwnerID: "alice-id",
		Status:  "archived",
	})
	env.seedWorkspace(t, &Workspace{
		Slug:    "active-two",
		GitURL:  "https://github.com/org/repo3",
		OwnerID: "alice-id",
		Status:  "active",
	})
	env.seedWorkspace(t, &Workspace{
		Slug:    "archived-two",
		GitURL:  "https://github.com/org/repo4",
		OwnerID: "alice-id",
		Status:  "archived",
	})

	t.Run("without include_archived", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", userAuth("alice-id"))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
		}
		workspaces := parseWorkspaceListJSON(t, rec)
		for _, ws := range workspaces {
			if ws.Status == "archived" {
				t.Errorf("workspace %q has status 'archived'; should be excluded", ws.Slug)
			}
		}
	})

	t.Run("include_archived=false", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces?include_archived=false", "",
			userAuth("alice-id"))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
		}
		workspaces := parseWorkspaceListJSON(t, rec)
		for _, ws := range workspaces {
			if ws.Status == "archived" {
				t.Errorf("workspace %q has status 'archived'; should be excluded with include_archived=false",
					ws.Slug)
			}
		}
	})

	t.Run("admin without include_archived", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", adminAuth())
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
		}
		workspaces := parseWorkspaceListJSON(t, rec)
		for _, ws := range workspaces {
			if ws.Status == "archived" {
				t.Errorf("admin listing: workspace %q has status 'archived'; should be excluded", ws.Slug)
			}
		}
	})
}

// TS-01-P7: For any workspace record in the workspaces table, created_at and
// updated_at are always valid RFC 3339 timestamp strings and are never null
// or empty.
// Property: 01-PROP-7
// Validates: 01-REQ-1.10, 01-REQ-5.1
func TestPropWorkspace_RFC3339Timestamps(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")

	// Create a workspace.
	body := `{"slug":"ts-prop-ws","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d; want %d", rec.Code, http.StatusCreated)
	}
	assertAllTimestampsValid(t, env)

	// Archive.
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ts-prop-ws/archive", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive: status = %d; want %d", rec.Code, http.StatusOK)
	}
	assertAllTimestampsValid(t, env)

	// Reactivate.
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ts-prop-ws/reactivate", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivate: status = %d; want %d", rec.Code, http.StatusOK)
	}
	assertAllTimestampsValid(t, env)
}

// assertAllTimestampsValid queries all workspace rows and verifies that
// created_at and updated_at are valid RFC 3339 timestamps.
func assertAllTimestampsValid(t *testing.T, env *testEnv) {
	t.Helper()
	rows, err := env.db.Query("SELECT slug, created_at, updated_at FROM workspaces")
	if err != nil {
		t.Fatalf("timestamp query failed: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var slug, createdAt, updatedAt string
		if err := rows.Scan(&slug, &createdAt, &updatedAt); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		if createdAt == "" {
			t.Errorf("workspace %q: created_at is empty", slug)
		} else if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
			t.Errorf("workspace %q: created_at %q is not valid RFC 3339: %v", slug, createdAt, err)
		}
		if updatedAt == "" {
			t.Errorf("workspace %q: updated_at is empty", slug)
		} else if _, err := time.Parse(time.RFC3339, updatedAt); err != nil {
			t.Errorf("workspace %q: updated_at %q is not valid RFC 3339: %v", slug, updatedAt, err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}
}

// TS-01-P8: For any request for a workspace the credential cannot access,
// the API always returns HTTP 404 regardless of whether the workspace exists,
// never HTTP 403.
// Property: 01-PROP-8
// Validates: 01-REQ-4.6, 01-REQ-7.3
func TestPropWorkspace_SlugEnumerationPrevention(t *testing.T) {
	env := newTestEnv(t)

	// Bob owns a workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "bob-ws",
		GitURL:  "https://github.com/org/bob-repo",
		OwnerID: "bob-id",
		Status:  "active",
	})

	// Also seed an archived workspace for bob.
	env.seedWorkspace(t, &Workspace{
		Slug:    "bob-archived",
		GitURL:  "https://github.com/org/bob-archived",
		OwnerID: "bob-id",
		Status:  "archived",
	})

	// Alice's credential should get 404 for all operations on Bob's
	// workspaces, never 403 — to prevent slug enumeration.
	aliceAuth := userAuth("alice-id")

	operations := []struct {
		name   string
		method string
		path   string
	}{
		{"GET existing workspace", http.MethodGet, "/api/v1/workspaces/bob-ws"},
		{"GET nonexistent workspace", http.MethodGet, "/api/v1/workspaces/nonexistent-slug"},
		{"archive existing workspace", http.MethodPost, "/api/v1/workspaces/bob-ws/archive"},
		{"archive nonexistent workspace", http.MethodPost, "/api/v1/workspaces/nonexistent-slug/archive"},
		{"reactivate existing workspace", http.MethodPost, "/api/v1/workspaces/bob-archived/reactivate"},
		{"reactivate nonexistent workspace", http.MethodPost, "/api/v1/workspaces/nonexistent-slug/reactivate"},
		{"delete existing workspace", http.MethodDelete, "/api/v1/workspaces/bob-archived"},
		{"delete nonexistent workspace", http.MethodDelete, "/api/v1/workspaces/nonexistent-slug"},
	}

	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			rec := env.doRequest(t, op.method, op.path, "", aliceAuth)
			if rec.Code == http.StatusForbidden {
				t.Errorf("%s %s: returned 403; must return 404 to prevent slug enumeration",
					op.method, op.path)
			}
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status = %d; want %d",
					op.method, op.path, rec.Code, http.StatusNotFound)
			}

			// Verify the error envelope uses 404 code.
			var envelope errorEnvelope
			if err := json.NewDecoder(rec.Body).Decode(&envelope); err == nil {
				if envelope.Error.Code == http.StatusForbidden {
					t.Errorf("%s %s: error.code = 403; must be 404", op.method, op.path)
				}
				if envelope.Error.Code != http.StatusNotFound {
					t.Errorf("%s %s: error.code = %d; want %d",
						op.method, op.path, envelope.Error.Code, http.StatusNotFound)
				}
			}
		})
	}
}
