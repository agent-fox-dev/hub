package workspace

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// newOrgHub is the real hub (apikit server, schema and workspace handlers)
// with org "acme" (id org-acme) holding u1's workspace W with one patch, and
// user u2, a member of acme, with an API key.
func newOrgHub(t *testing.T) (h *realHub, memberKey string) {
	t.Helper()
	h = newRealHub(t)
	db := h.db.SqlDB
	now := apikit.NowUTC()
	for _, stmt := range []string{
		`INSERT INTO users (id, username, email, full_name, role, status, provider, provider_id, created_at, updated_at) VALUES ('u2', 'u2', 'u2@example.com', '', 'user', 'active', 'github', 'p2', '` + now + `', '` + now + `')`,
		`INSERT INTO orgs (id, name, slug, status, created_at, updated_at) VALUES ('org-acme', 'Acme', 'acme', 'active', '` + now + `', '` + now + `')`,
		`INSERT INTO org_members (org_id, user_id, created_at) VALUES ('org-acme', 'u1', '` + now + `'), ('org-acme', 'u2', '` + now + `')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := insertWorkspace(db, &Workspace{Slug: "team-w", GitURL: "https://github.com/acme/w", OwnerID: "u1", OrgID: strp("org-acme"),
		Status: "ready", WorkspaceMode: "carry_patch",
		UpstreamURL: strp("https://github.com/up/w"), IntegrationBranch: strp("deploy")}); err != nil {
		t.Fatal(err)
	}
	seedPatchRaw(t, db, "patch-1", "team-w", "feature/a", 1)
	key, err := apikit.GenerateAPIKey(db, "u2", 0, echo.New().Logger)
	if err != nil {
		t.Fatal(err)
	}
	return h, key.FullKey
}

// TS-35-57 (smoke): A member finds a teammate's workspace in the listing,
// with access member and the org's slug, and reads its patch stack.
// Path: 35-PATH-1. Requirements: 35-REQ-4.2, 35-REQ-6.2
func TestSmoke_MemberListsAndReadsPatches_TS_35_57(t *testing.T) {
	h, member := newOrgHub(t)
	rec := h.do(http.MethodGet, "/api/v1/workspaces", member, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("listing: %d %s", rec.Code, rec.Body)
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0]["slug"] != "team-w" || list[0]["access"] != "member" || list[0]["org_slug"] != "acme" {
		t.Fatalf("listing %v; want team-w with access member and org_slug acme", list)
	}
	rec = h.do(http.MethodGet, "/api/v1/workspaces/team-w/patches", member, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("patches: %d %s", rec.Code, rec.Body)
	}
	var patches []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &patches); err != nil {
		t.Fatal(err)
	}
	found := len(patches) == 1 && patches[0]["id"] == "patch-1"
	if !found {
		t.Errorf("patches body %s; want W's patch stack with patch-1", rec.Body)
	}
}

// TS-35-60 (smoke): A removed member loses read access on the next request.
// Path: 35-PATH-4. Requirement: 35-REQ-1.6
func TestSmoke_RemovedMemberLosesAccess_TS_35_60(t *testing.T) {
	h, member := newOrgHub(t)
	rec := h.do(http.MethodGet, "/api/v1/workspaces/team-w", member, "")
	var ws map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &ws) != nil || ws["access"] != "member" {
		t.Fatalf("member GET: %d %s; want 200 with access member", rec.Code, rec.Body)
	}
	if _, err := h.db.SqlDB.Exec(`DELETE FROM org_members WHERE org_id = 'org-acme' AND user_id = 'u2'`); err != nil {
		t.Fatal(err)
	}
	rec = h.do(http.MethodGet, "/api/v1/workspaces/team-w", member, "")
	if rec.Code != http.StatusNotFound || parseErrorEnvelope(t, rec).Error.Message != "workspace not found" {
		t.Errorf("after removal: %d %s; want 404 workspace not found", rec.Code, rec.Body)
	}
}
