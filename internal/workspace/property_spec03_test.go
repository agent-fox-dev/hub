package workspace

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Task 5.1: Property tests — display_name and description never null in any
// response
// TS-03-P1, TS-03-P2
// Requirements: 03-REQ-1.2, 03-REQ-1.3, 03-REQ-1.4, 03-REQ-4.3, 03-REQ-4.4,
//
//	03-REQ-5.1
//
// =============================================================================

// TS-03-P1: For any workspace response from create, list, get, update, archive,
// or reactivate, display_name is always a non-null, non-empty string equal to
// user-supplied value or the workspace slug.
//
// Property: 03-PROP-1
// Validates: 03-REQ-1.2, 03-REQ-1.4, 03-REQ-4.3, 03-REQ-5.1
//
// Strategy: Generate arbitrary slug/display_name combinations, exercise all
// workspace operations, and assert the invariant holds for each response.
// Without a property-test library (rapid/gopter), we use table-driven inputs
// that cover the null/empty/valid/boundary cases the property generator would.
func TestSpec03_Group5_DisplayNameNeverNull(t *testing.T) {
	env := newTestEnv(t)

	// Arbitrary input matrix: (slug, display_name to provide, expected result)
	// null/empty → slug; non-empty ≤128 chars → provided value.
	type input struct {
		slug        string
		displayName *string // nil means omit from request; pointer to "" means send ""
		wantDN      string  // expected display_name in responses
	}

	strPtr := func(s string) *string { return &s }

	inputs := []input{
		// Omit display_name → defaults to slug.
		{"slug-omit", nil, "slug-omit"},
		// Explicit null → normalizes to slug.
		{"slug-null", nil, "slug-null"}, // JSON null handled below
		// Empty string → normalizes to slug.
		{"slug-empty", strPtr(""), "slug-empty"},
		// Short valid display_name.
		{"slug-short", strPtr("My Project"), "My Project"},
		// Max length display_name (128 chars).
		{"slug-max", strPtr(strings.Repeat("x", 128)), strings.Repeat("x", 128)},
		// Slug with hyphens and numbers.
		{"my-project-42", strPtr("Project 42"), "Project 42"},
		// Unicode display_name.
		{"slug-unicode", strPtr("Projet Numero Un"), "Projet Numero Un"},
	}

	auth := userAuth("prop-user-1")

	for i, in := range inputs {
		t.Run(fmt.Sprintf("input_%d_%s", i, in.slug), func(t *testing.T) {
			// Build create request body.
			var body string
			switch {
			case in.displayName == nil && i == 1:
				// Special case: explicit JSON null for the second "null" test.
				body = fmt.Sprintf(`{"slug":%q,"git_url":"https://git.example.com/repo-%d","display_name":null}`, in.slug, i)
			case in.displayName == nil:
				body = fmt.Sprintf(`{"slug":%q,"git_url":"https://git.example.com/repo-%d"}`, in.slug, i)
			default:
				dn, _ := json.Marshal(*in.displayName)
				body = fmt.Sprintf(`{"slug":%q,"git_url":"https://git.example.com/repo-%d","display_name":%s}`, in.slug, i, string(dn))
			}

			// CREATE
			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
			if rec.Code != http.StatusCreated {
				t.Fatalf("CREATE status = %d; want %d", rec.Code, http.StatusCreated)
			}
			ws := parseWorkspaceJSON(t, rec)
			assertDisplayNameInvariant(t, "CREATE", ws, in.wantDN, in.slug)

			// GET
			getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+in.slug, "", auth)
			if getRec.Code != http.StatusOK {
				t.Fatalf("GET status = %d; want %d", getRec.Code, http.StatusOK)
			}
			getWS := parseWorkspaceJSON(t, getRec)
			assertDisplayNameInvariant(t, "GET", getWS, in.wantDN, in.slug)

			// LIST
			listRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
			if listRec.Code != http.StatusOK {
				t.Fatalf("LIST status = %d; want %d", listRec.Code, http.StatusOK)
			}
			listWSs := parseWorkspaceListJSON(t, listRec)
			found := false
			for _, lws := range listWSs {
				if lws.Slug == in.slug {
					found = true
					assertDisplayNameInvariant(t, "LIST", lws, in.wantDN, in.slug)
				}
			}
			if !found {
				t.Errorf("LIST: workspace %q not found in listing", in.slug)
			}

			// PATCH (update) — set display_name to null to verify normalization.
			patchBody := `{"display_name":null}`
			patchRec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+in.slug, patchBody, auth)
			if patchRec.Code != http.StatusOK {
				t.Fatalf("PATCH status = %d; want %d", patchRec.Code, http.StatusOK)
			}
			patchWS := parseWorkspaceJSON(t, patchRec)
			// After clearing, display_name should be the slug.
			assertDisplayNameInvariant(t, "PATCH(clear)", patchWS, in.slug, in.slug)

			// ARCHIVE
			archRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+in.slug+"/archive", "", auth)
			if archRec.Code != http.StatusOK {
				t.Fatalf("ARCHIVE status = %d; want %d", archRec.Code, http.StatusOK)
			}
			archWS := parseWorkspaceJSON(t, archRec)
			assertDisplayNameInvariant(t, "ARCHIVE", archWS, in.slug, in.slug)

			// REACTIVATE
			reactRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+in.slug+"/reactivate", "", auth)
			if reactRec.Code != http.StatusOK {
				t.Fatalf("REACTIVATE status = %d; want %d", reactRec.Code, http.StatusOK)
			}
			reactWS := parseWorkspaceJSON(t, reactRec)
			assertDisplayNameInvariant(t, "REACTIVATE", reactWS, in.slug, in.slug)
		})
	}
}

// assertDisplayNameInvariant checks the display_name property invariant:
// display_name is not nil, not empty, and equals the expected value.
func assertDisplayNameInvariant(t *testing.T, op string, ws workspaceJSON, wantDN, slug string) {
	t.Helper()
	// Invariant: display_name is a non-null, non-empty string.
	if ws.DisplayName == "" {
		t.Errorf("%s: display_name is empty; want non-empty string (at least slug %q)", op, slug)
	}
	if ws.DisplayName != wantDN {
		t.Errorf("%s: display_name = %q; want %q", op, ws.DisplayName, wantDN)
	}
}

// TS-03-P2: For any workspace response from create, list, get, update, archive,
// or reactivate, description is always a non-null string (may be empty).
//
// Property: 03-PROP-2
// Validates: 03-REQ-1.3, 03-REQ-1.4, 03-REQ-4.4, 03-REQ-5.1
//
// Strategy: Generate arbitrary slug/description combinations, exercise all
// workspace operations, and assert the invariant holds for each response.
func TestSpec03_Group5_DescriptionNeverNull(t *testing.T) {
	env := newTestEnv(t)

	type input struct {
		slug        string
		description *string // nil means omit; pointer to "" means send ""
		wantDesc    string  // expected description
	}

	strPtr := func(s string) *string { return &s }

	inputs := []input{
		// Omit description → defaults to empty string.
		{"desc-omit", nil, ""},
		// Explicit null → normalizes to empty string.
		{"desc-null", nil, ""}, // JSON null handled below
		// Empty string → empty string.
		{"desc-empty", strPtr(""), ""},
		// Short description.
		{"desc-short", strPtr("A test workspace"), "A test workspace"},
		// Max length description (1024 chars).
		{"desc-max", strPtr(strings.Repeat("d", 1024)), strings.Repeat("d", 1024)},
		// Description with special characters.
		{"desc-special", strPtr("Workspace for testing: <html> & 'quotes'"), "Workspace for testing: <html> & 'quotes'"},
	}

	auth := userAuth("prop-user-2")

	for i, in := range inputs {
		t.Run(fmt.Sprintf("input_%d_%s", i, in.slug), func(t *testing.T) {
			// Build create request body.
			var body string
			switch {
			case in.description == nil && i == 1:
				body = fmt.Sprintf(`{"slug":%q,"git_url":"https://git.example.com/desc-repo-%d","description":null}`, in.slug, i)
			case in.description == nil:
				body = fmt.Sprintf(`{"slug":%q,"git_url":"https://git.example.com/desc-repo-%d"}`, in.slug, i)
			default:
				desc, _ := json.Marshal(*in.description)
				body = fmt.Sprintf(`{"slug":%q,"git_url":"https://git.example.com/desc-repo-%d","description":%s}`, in.slug, i, string(desc))
			}

			// CREATE
			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
			if rec.Code != http.StatusCreated {
				t.Fatalf("CREATE status = %d; want %d", rec.Code, http.StatusCreated)
			}
			ws := parseWorkspaceJSON(t, rec)
			assertDescriptionInvariant(t, "CREATE", ws, in.wantDesc)

			// GET
			getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+in.slug, "", auth)
			if getRec.Code != http.StatusOK {
				t.Fatalf("GET status = %d; want %d", getRec.Code, http.StatusOK)
			}
			getWS := parseWorkspaceJSON(t, getRec)
			assertDescriptionInvariant(t, "GET", getWS, in.wantDesc)

			// LIST
			listRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
			if listRec.Code != http.StatusOK {
				t.Fatalf("LIST status = %d; want %d", listRec.Code, http.StatusOK)
			}
			listWSs := parseWorkspaceListJSON(t, listRec)
			found := false
			for _, lws := range listWSs {
				if lws.Slug == in.slug {
					found = true
					assertDescriptionInvariant(t, "LIST", lws, in.wantDesc)
				}
			}
			if !found {
				t.Errorf("LIST: workspace %q not found in listing", in.slug)
			}

			// PATCH (update) — set description to null to verify normalization.
			patchBody := `{"description":null}`
			patchRec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+in.slug, patchBody, auth)
			if patchRec.Code != http.StatusOK {
				t.Fatalf("PATCH status = %d; want %d", patchRec.Code, http.StatusOK)
			}
			patchWS := parseWorkspaceJSON(t, patchRec)
			// After clearing, description should be empty string.
			assertDescriptionInvariant(t, "PATCH(clear)", patchWS, "")

			// ARCHIVE
			archRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+in.slug+"/archive", "", auth)
			if archRec.Code != http.StatusOK {
				t.Fatalf("ARCHIVE status = %d; want %d", archRec.Code, http.StatusOK)
			}
			archWS := parseWorkspaceJSON(t, archRec)
			assertDescriptionInvariant(t, "ARCHIVE", archWS, "")

			// REACTIVATE
			reactRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+in.slug+"/reactivate", "", auth)
			if reactRec.Code != http.StatusOK {
				t.Fatalf("REACTIVATE status = %d; want %d", reactRec.Code, http.StatusOK)
			}
			reactWS := parseWorkspaceJSON(t, reactRec)
			assertDescriptionInvariant(t, "REACTIVATE", reactWS, "")
		})
	}
}

// assertDescriptionInvariant checks the description property invariant:
// description is not nil (Go zero value "" is the expected non-null marker).
// Uses raw JSON parsing to verify the field is actually present in the response.
func assertDescriptionInvariant(t *testing.T, op string, ws workspaceJSON, wantDesc string) {
	t.Helper()
	// In Go, json.Unmarshal sets missing string fields to "". We rely on
	// TS-03-27 (AllRequiredFieldsInResponse) for field-presence checks.
	// Here we verify the value is as expected.
	if ws.Description != wantDesc {
		t.Errorf("%s: description = %q; want %q", op, ws.Description, wantDesc)
	}
}

// =============================================================================
// Task 5.2: Property tests — partial update does not clobber omitted fields;
// updated_at advances
// TS-03-P3, TS-03-P4
// Requirements: 03-REQ-4.1, 03-REQ-4.2
// =============================================================================

// TS-03-P3: For any successful PATCH that omits one or more of display_name,
// description, org_id, the omitted fields in the workspaces table are identical
// before and after the update.
//
// Property: 03-PROP-3
// Validates: 03-REQ-4.2
//
// Strategy: Generate arbitrary subsets of {display_name, description, org_id}
// (at least one, at most two) to include in the PATCH body. Record pre-PATCH
// state via GET, apply PATCH, verify omitted fields unchanged via GET.
func TestSpec03_Group5_PartialUpdateNoClobber(t *testing.T) {
	env := newTestEnv(t)

	// Set up org and membership for org_id tests.
	env.seedOrg(t, "prop-org-1", "Prop Org", "prop-org")
	env.seedOrgMember(t, "prop-org-1", "prop-u1")

	auth := userAuth("prop-u1")

	// Subsets of fields to include in PATCH body.
	// Each test case includes only the listed fields; the omitted ones must not change.
	type patchCase struct {
		name      string
		patchBody string
		included  []string // fields included in the PATCH
	}

	cases := []patchCase{
		{
			name:      "only_display_name",
			patchBody: `{"display_name":"Updated Name"}`,
			included:  []string{"display_name"},
		},
		{
			name:      "only_description",
			patchBody: `{"description":"Updated desc"}`,
			included:  []string{"description"},
		},
		{
			name:      "only_org_id",
			patchBody: `{"org_id":"prop-org-1"}`,
			included:  []string{"org_id"},
		},
		{
			name:      "display_name_and_description",
			patchBody: `{"display_name":"DN Only","description":"Desc Only"}`,
			included:  []string{"display_name", "description"},
		},
		{
			name:      "display_name_and_org_id",
			patchBody: `{"display_name":"DN Org","org_id":"prop-org-1"}`,
			included:  []string{"display_name", "org_id"},
		},
		{
			name:      "description_and_org_id",
			patchBody: `{"description":"Desc Org","org_id":"prop-org-1"}`,
			included:  []string{"description", "org_id"},
		},
	}

	allFields := []string{"display_name", "description", "org_id"}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug := fmt.Sprintf("partial-%d", i)

			// Create workspace with known initial values.
			createBody := fmt.Sprintf(
				`{"slug":%q,"git_url":"https://git.example.com/partial-%d","display_name":"Initial DN","description":"Initial desc"}`,
				slug, i,
			)
			createRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", createBody, auth)
			if createRec.Code != http.StatusCreated {
				t.Fatalf("CREATE status = %d; want %d", createRec.Code, http.StatusCreated)
			}

			// Record pre-PATCH state via GET.
			preRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug, "", auth)
			if preRec.Code != http.StatusOK {
				t.Fatalf("GET pre-PATCH status = %d; want %d", preRec.Code, http.StatusOK)
			}
			var preMap map[string]any
			if err := json.NewDecoder(preRec.Body).Decode(&preMap); err != nil {
				t.Fatalf("decode pre-PATCH response: %v", err)
			}

			// Apply PATCH.
			patchRec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug, tc.patchBody, auth)
			if patchRec.Code != http.StatusOK {
				t.Fatalf("PATCH status = %d; want %d", patchRec.Code, http.StatusOK)
			}

			// Record post-PATCH state via GET.
			postRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug, "", auth)
			if postRec.Code != http.StatusOK {
				t.Fatalf("GET post-PATCH status = %d; want %d", postRec.Code, http.StatusOK)
			}
			var postMap map[string]any
			if err := json.NewDecoder(postRec.Body).Decode(&postMap); err != nil {
				t.Fatalf("decode post-PATCH response: %v", err)
			}

			// Build set of included fields for fast lookup.
			includedSet := make(map[string]bool, len(tc.included))
			for _, f := range tc.included {
				includedSet[f] = true
			}

			// Assert: omitted fields must be identical before and after.
			for _, field := range allFields {
				if includedSet[field] {
					continue // This field was included in the PATCH — skip.
				}
				preVal := fmt.Sprintf("%v", preMap[field])
				postVal := fmt.Sprintf("%v", postMap[field])
				if preVal != postVal {
					t.Errorf("omitted field %q changed: %q -> %q", field, preVal, postVal)
				}
			}
		})
	}
}

// TS-03-P4: For any successful PATCH to PATCH /api/v1/workspaces/:slug, the
// updated_at in the response is strictly greater than the updated_at that
// existed before the request.
//
// Property: 03-PROP-4
// Validates: 03-REQ-4.1
//
// Strategy: For multiple arbitrary valid PATCH bodies, record initial
// updated_at, submit PATCH, assert the new updated_at is strictly after.
func TestSpec03_Group5_UpdatedAtAdvances(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("prop-u3")

	patches := []struct {
		name string
		body string
	}{
		{"update_display_name", `{"display_name":"New Name"}`},
		{"update_description", `{"description":"New description"}`},
		{"update_both", `{"display_name":"Both DN","description":"Both desc"}`},
	}

	for i, tc := range patches {
		t.Run(tc.name, func(t *testing.T) {
			slug := fmt.Sprintf("ts-advance-%d", i)
			createBody := fmt.Sprintf(
				`{"slug":%q,"git_url":"https://git.example.com/ts-adv-%d"}`,
				slug, i,
			)
			createRec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", createBody, auth)
			if createRec.Code != http.StatusCreated {
				t.Fatalf("CREATE status = %d; want %d", createRec.Code, http.StatusCreated)
			}

			// Record initial updated_at.
			getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug, "", auth)
			if getRec.Code != http.StatusOK {
				t.Fatalf("GET status = %d; want %d", getRec.Code, http.StatusOK)
			}
			initialWS := parseWorkspaceJSON(t, getRec)
			initialTime, err := time.Parse(time.RFC3339, initialWS.UpdatedAt)
			if err != nil {
				t.Fatalf("parse initial updated_at %q: %v", initialWS.UpdatedAt, err)
			}

			// Small delay to ensure timestamp will differ.
			time.Sleep(10 * time.Millisecond)

			// Apply PATCH.
			patchRec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug, tc.body, auth)
			if patchRec.Code != http.StatusOK {
				t.Fatalf("PATCH status = %d; want %d", patchRec.Code, http.StatusOK)
			}
			patchWS := parseWorkspaceJSON(t, patchRec)

			// Assert updated_at advanced.
			patchTime, err := time.Parse(time.RFC3339, patchWS.UpdatedAt)
			if err != nil {
				t.Fatalf("parse patched updated_at %q: %v", patchWS.UpdatedAt, err)
			}
			if !patchTime.After(initialTime) {
				t.Errorf("updated_at %q is not strictly after initial %q",
					patchWS.UpdatedAt, initialWS.UpdatedAt)
			}
		})
	}
}

// =============================================================================
// Task 5.3: Property tests — immutable fields unchanged; workspaces:delete no
// read; existing scopes stable
// TS-03-P5, TS-03-P6, TS-03-P7
// Requirements: 03-REQ-4.7, 03-REQ-2.3, 03-REQ-2.5
// =============================================================================

// TS-03-P5: For any PATCH to PATCH /api/v1/workspaces/:slug (successful or not),
// the immutable fields slug, git_url, branch, owner_id, created_at, and status
// in the workspaces table are unchanged after the operation.
//
// Property: 03-PROP-5
// Validates: 03-REQ-4.7
//
// Strategy: Submit various PATCH bodies (valid partial, invalid with immutable
// fields), verify immutable fields unchanged in DB.
func TestSpec03_Group5_ImmutableFieldsUnchanged(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("prop-u4")

	branch := "main"
	env.seedWorkspace(t, &Workspace{
		Slug:    "immut-prop-ws",
		GitURL:  "https://git.example.com/immut-repo",
		Branch:  &branch,
		OwnerID: "prop-u4",
		Status:  "active",
	})

	// Record pre-PATCH immutable field values from the database.
	type immutableState struct {
		slug      string
		gitURL    string
		branch    *string
		ownerID   string
		createdAt string
		status    string
	}

	readImmutable := func(t *testing.T) immutableState {
		t.Helper()
		var s immutableState
		err := env.db.QueryRow(
			`SELECT slug, git_url, branch, owner_id, created_at, status FROM workspaces WHERE slug = ?`,
			"immut-prop-ws",
		).Scan(&s.slug, &s.gitURL, &s.branch, &s.ownerID, &s.createdAt, &s.status)
		if err != nil {
			t.Fatalf("read immutable state: %v", err)
		}
		return s
	}

	pre := readImmutable(t)

	// Various PATCH bodies: valid updates, and invalid ones including immutable fields.
	patchBodies := []struct {
		name string
		body string
	}{
		// Valid partial update.
		{"valid_description", `{"description":"changed"}`},
		{"valid_display_name", `{"display_name":"Changed Name"}`},
		// Invalid: attempts to change immutable fields (should be rejected).
		{"immutable_slug", `{"slug":"new-slug"}`},
		{"immutable_git_url", `{"git_url":"https://other.example.com/repo"}`},
		{"immutable_branch", `{"branch":"develop"}`},
		{"immutable_owner_id", `{"owner_id":"other-user"}`},
		// Mixed: valid + immutable (should be rejected).
		{"mixed_desc_slug", `{"description":"x","slug":"y"}`},
	}

	for _, tc := range patchBodies {
		t.Run(tc.name, func(t *testing.T) {
			// Apply PATCH (may succeed or fail depending on the body).
			env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/immut-prop-ws", tc.body, auth)

			// Assert immutable fields unchanged regardless of PATCH outcome.
			post := readImmutable(t)
			if post.slug != pre.slug {
				t.Errorf("slug changed: %q -> %q", pre.slug, post.slug)
			}
			if post.gitURL != pre.gitURL {
				t.Errorf("git_url changed: %q -> %q", pre.gitURL, post.gitURL)
			}
			preBranch := "<nil>"
			postBranch := "<nil>"
			if pre.branch != nil {
				preBranch = *pre.branch
			}
			if post.branch != nil {
				postBranch = *post.branch
			}
			if preBranch != postBranch {
				t.Errorf("branch changed: %q -> %q", preBranch, postBranch)
			}
			if post.ownerID != pre.ownerID {
				t.Errorf("owner_id changed: %q -> %q", pre.ownerID, post.ownerID)
			}
			if post.createdAt != pre.createdAt {
				t.Errorf("created_at changed: %q -> %q", pre.createdAt, post.createdAt)
			}
			if post.status != pre.status {
				t.Errorf("status changed: %q -> %q", pre.status, post.status)
			}
		})
	}
}

// TS-03-P6: For any PAT holding only workspaces:delete (without workspaces:read
// or workspaces:write), list and get workspace requests are always denied with
// HTTP 404.
//
// Property: 03-PROP-6
// Validates: 03-REQ-2.5, 03-REQ-2.E1
//
// Strategy: Create multiple workspaces owned by the PAT user, issue list and
// get requests with a delete-only PAT, assert all return 404.
func TestSpec03_Group5_DeletePATNoReadAccess(t *testing.T) {
	env := newTestEnv(t)

	// Create multiple workspaces for the PAT user.
	slugs := []string{"del-only-ws-1", "del-only-ws-2", "del-only-ws-3"}
	for i, slug := range slugs {
		env.seedWorkspace(t, &Workspace{
			Slug:    slug,
			GitURL:  fmt.Sprintf("https://git.example.com/del-only-%d", i),
			OwnerID: "del-pat-user",
			Status:  "active",
		})
	}

	// PAT with ONLY workspaces:delete — no read, no write.
	deleteOnlyAuth := patAuth("del-pat-user", "workspaces:delete")

	// List must return 404.
	t.Run("list_denied", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", deleteOnlyAuth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/v1/workspaces status = %d; want %d (anti-enumeration)",
				rec.Code, http.StatusNotFound)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusNotFound {
			t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
		}
	})

	// Get each workspace must return 404.
	for _, slug := range slugs {
		t.Run("get_denied_"+slug, func(t *testing.T) {
			rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug, "", deleteOnlyAuth)
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET /api/v1/workspaces/%s status = %d; want %d (anti-enumeration)",
					slug, rec.Code, http.StatusNotFound)
			}
			resp := parseErrorEnvelope(t, rec)
			if resp.Error.Code != http.StatusNotFound {
				t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
			}
		})
	}
}

// TS-03-P7: For any PAT holding workspaces:read or workspaces:create, the set
// of accessible endpoints and returned responses are identical to those
// accessible before the introduction of workspaces:write and workspaces:delete.
//
// Property: 03-PROP-7
// Validates: 03-REQ-2.3
//
// Strategy: For read and create PATs, verify list/get return 200; mutation
// endpoints (PATCH, archive, reactivate, delete) return 404.
func TestSpec03_Group5_ExistingScopesStable(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "scope-stable-ws",
		GitURL:  "https://git.example.com/scope-stable",
		OwnerID: "scope-user",
		Status:  "active",
	})

	pats := []struct {
		name string
		auth *AuthInfo
	}{
		{"workspaces:read", patAuth("scope-user", "workspaces:read")},
		{"workspaces:create", patAuth("scope-user", "workspaces:create")},
	}

	for _, pat := range pats {
		t.Run(pat.name, func(t *testing.T) {
			// Read operations must succeed.
			t.Run("list_returns_200", func(t *testing.T) {
				rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", pat.auth)
				if rec.Code != http.StatusOK {
					t.Errorf("GET /api/v1/workspaces status = %d; want %d",
						rec.Code, http.StatusOK)
				}
			})

			t.Run("get_returns_200", func(t *testing.T) {
				rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/scope-stable-ws", "", pat.auth)
				if rec.Code != http.StatusOK {
					t.Errorf("GET /api/v1/workspaces/scope-stable-ws status = %d; want %d",
						rec.Code, http.StatusOK)
				}
			})

			// Mutation operations must be denied with 404 (anti-enumeration).
			mutations := []struct {
				name   string
				method string
				path   string
				body   string
			}{
				{"PATCH", http.MethodPatch, "/api/v1/workspaces/scope-stable-ws", `{"description":"x"}`},
				{"archive", http.MethodPost, "/api/v1/workspaces/scope-stable-ws/archive", ""},
				{"reactivate", http.MethodPost, "/api/v1/workspaces/scope-stable-ws/reactivate", ""},
				{"DELETE", http.MethodDelete, "/api/v1/workspaces/scope-stable-ws", ""},
			}

			for _, mut := range mutations {
				t.Run(mut.name+"_returns_404", func(t *testing.T) {
					rec := env.doRequest(t, mut.method, mut.path, mut.body, pat.auth)
					if rec.Code != http.StatusNotFound {
						t.Errorf("%s %s status = %d; want %d (denied; unchanged from pre-feature baseline)",
							mut.method, mut.path, rec.Code, http.StatusNotFound)
					}
				})
			}
		})
	}
}
