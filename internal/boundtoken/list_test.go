package boundtoken

import (
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// Listing needs a credential that is not a bound token (reported), a PAT's
// tokens:read or tokens:manage, and the owner or an admin token; a member of
// the org and a stranger get the unknown workspace's 404.
func TestList_ChecksCallerInOrder(t *testing.T) {
	v := newEnv(t)
	testutil.BindToken(t, v.db, "bound", slug)
	refusals := captureRefusals(t)
	for _, c := range []struct {
		name, path   string
		auth         *apikit.AuthInfo
		status       int
		message, typ string
	}{
		{"no credential", mintPath, nil, http.StatusUnauthorized, "authentication required", ""},
		{"bound PAT, even to this workspace", mintPath, testutil.BoundPAT(owner, "bound", "tokens:read"), http.StatusForbidden, "this token is bound to workspace s", wsaccess.ErrorTypeTokenBound},
		{"PAT without scope", mintPath, testutil.PAT(owner, "workspaces:read"), http.StatusForbidden, "PAT requires tokens:read scope", ""},
		{"member", mintPath, testutil.User(member), http.StatusNotFound, "workspace not found", ""},
		{"stranger", mintPath, testutil.PAT(stranger, "tokens:read"), http.StatusNotFound, "workspace not found", ""},
		{"unknown", "/api/v1/workspaces/ghost/tokens", testutil.Admin(), http.StatusNotFound, "workspace not found", ""},
		{"owner", mintPath, testutil.PAT(owner, "tokens:manage"), http.StatusOK, "", ""},
		{"admin", mintPath, testutil.Admin(), http.StatusOK, "", ""},
	} {
		rec := testutil.Request(t, v.e, http.MethodGet, c.path, "", c.auth)
		if rec.Code != c.status {
			t.Errorf("%s: %d %s; want %d", c.name, rec.Code, rec.Body, c.status)
		} else if c.message != "" {
			wantRefusal(t, rec, c.status, c.message, c.typ)
		}
	}
	if len(*refusals) != 1 || (*refusals)[0].TokenID != "bound" || (*refusals)[0].Route != "/api/v1/workspaces/:slug/tokens" || (*refusals)[0].Status != http.StatusForbidden {
		t.Errorf("refusals %+v, want the bound PAT's one", *refusals)
	}
}

// The listing is every token bound to the workspace joined with its pats
// row, oldest first, revoked and expired included and bindings without a row
// left out: seven fields, permissions as an array ([] when unreadable), null
// for unset times, and never a secret. An archived workspace lists; an empty
// one is []; a failed query is 500.
func TestList_RendersBoundTokens(t *testing.T) {
	v := newEnv(t)
	testutil.SeedWorkspace(t, v.db, testutil.Workspace{Slug: "old", OwnerID: owner, Status: "archived"})
	v.seedPAT(t, "c-live", "live", `["a","b"]`, "", "", "2026-09-01T10:00:02Z")
	v.seedPAT(t, "b-revoked", "revoked", `not json`, "2026-12-01T00:00:00Z", "2026-10-01T00:00:00Z", "2026-09-01T10:00:01Z")
	v.seedPAT(t, "a-expired", "expired", `[]`, "2026-09-01T11:00:00Z", "", "2026-09-01T10:00:01Z")
	for _, id := range []string{"c-live", "b-revoked", "a-expired", "orphan"} {
		testutil.BindToken(t, v.db, id, "old")
	}
	rec := testutil.Request(t, v.e, http.MethodGet, "/api/v1/workspaces/old/tokens", "", testutil.User(owner))
	list := testutil.Objects(t, rec)
	if rec.Code != http.StatusOK || len(list) != 3 || list[0]["token_id"] != "a-expired" || list[1]["token_id"] != "b-revoked" || list[2]["token_id"] != "c-live" {
		t.Fatalf("listing %d %s", rec.Code, rec.Body)
	}
	live, revoked := list[2], list[1]
	if len(live) != 7 || live["name"] != "live" || live["workspace"] != "old" || live["created_at"] != "2026-09-01T10:00:02Z" ||
		live["expires_at"] != nil || live["revoked_at"] != nil || len(live["permissions"].([]any)) != 2 {
		t.Errorf("live token %v", live)
	}
	if revoked["expires_at"] != "2026-12-01T00:00:00Z" || revoked["revoked_at"] != "2026-10-01T00:00:00Z" || len(revoked["permissions"].([]any)) != 0 {
		t.Errorf("revoked token %v", revoked)
	}
	if body := rec.Body.String(); strings.Contains(body, "hash") || strings.Contains(body, `"token"`) {
		t.Errorf("the listing carries a secret: %s", body)
	}
	if rec := testutil.Request(t, v.e, http.MethodGet, mintPath, "", testutil.User(owner)); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty workspace: %s, want []", rec.Body)
	}
	testutil.Exec(t, v.db, `DROP TABLE pats`)
	wantRefusal(t, testutil.Request(t, v.e, http.MethodGet, mintPath, "", testutil.User(owner)), http.StatusInternalServerError, "internal server error", "")
}
