package workspace

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

func patch(t *testing.T, v *env, slug, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	return v.do(t, http.MethodPatch, "/api/v1/workspaces/"+slug, body, auth)
}

// TestUpdate_MutableFields: a PATCH changes the fields it names, leaves the
// others, normalizes nulls, refreshes updated_at and records which fields
// changed.
func TestUpdate_MutableFields(t *testing.T) {
	v := newEnv(t)
	testutil.SeedOrg(t, v.db, "org-acme", "acme", "")
	testutil.SeedOrgMember(t, v.db, "org-acme", owner)
	ws := v.seed(t, &Workspace{Slug: "ws", OwnerID: owner, DisplayName: "Old", Description: "old"})
	before := v.column(t, "ws", "updated_at")

	rec := patch(t, v, "ws", `{"display_name":"New","sync_mode":"disabled","org_id":"acme","settings":{"rebuild_strategy":"merge"}}`, testutil.User(owner))
	expectStatus(t, rec, http.StatusOK)
	got := testutil.Object(t, rec)
	if got["display_name"] != "New" || got["description"] != "old" || got["sync_mode"] != "disabled" || got["org_id"] != "org-acme" ||
		got["org_slug"] != "acme" || got["access"] != "owner" || got["settings"].(map[string]any)["rebuild_strategy"] != "merge" ||
		got["git_url"] != ws.GitURL || got["updated_at"].(string) <= before {
		t.Errorf("response %v", got)
	}
	ev := v.events.one(t, "hub.workspace.update")
	if fields, _ := ev.Metadata["fields"].([]string); strings.Join(fields, ",") != "display_name,org_id,sync_mode" || ev.Workspace != "ws" || ev.Action != "update" {
		t.Errorf("event %+v", ev)
	}

	// Nulls: the display name returns to the slug, the description to "", the
	// org is removed; the settings keep what the earlier PATCH set.
	rec = patch(t, v, "ws", `{"display_name":null,"description":null,"org_id":null}`, testutil.User(owner))
	expectStatus(t, rec, http.StatusOK)
	if got := testutil.Object(t, rec); got["display_name"] != "ws" || got["description"] != "" || got["org_id"] != nil || got["settings"].(map[string]any)["rebuild_strategy"] != "merge" {
		t.Errorf("after nulls %v", got)
	}
	// An empty org_id removes the org as well; a write PAT and an admin may
	// update.
	expectStatus(t, patch(t, v, "ws", `{"org_id":""}`, testutil.PAT(owner, "workspaces:write")), http.StatusOK)
	expectStatus(t, patch(t, v, "ws", `{"description":"by admin"}`, testutil.Admin()), http.StatusOK)
	if v.column(t, "ws", "description") != "by admin" {
		t.Error("the admin's update was not stored")
	}
}

// TestUpdate_Refusals: the body, the fields and the workspace's state are
// checked, each with its own answer.
func TestUpdate_Refusals(t *testing.T) {
	v := newEnv(t)
	v.orgWorkspace(t, "team")
	v.seed(t, &Workspace{Slug: "gone", OwnerID: owner, Status: "archived"})
	testutil.SeedOrg(t, v.db, "org-other", "other", "")
	swap(t, &orgMembershipCheckFn, func(db *sql.DB, userID, orgID string) (int, string) {
		if orgID == "flaky" {
			return http.StatusInternalServerError, "membership check timed out"
		}
		return checkOrgMembership(db, userID, orgID)
	})
	o := testutil.User(owner)
	cases := []struct {
		name string
		slug string
		body string
		auth *apikit.AuthInfo
		code int
		msg  string
	}{
		{"unauthenticated", "team", `{"description":"x"}`, nil, http.StatusUnauthorized, "authentication required"},
		{"read PAT", "team", `{"description":"x"}`, testutil.PAT(owner, "workspaces:read"), http.StatusNotFound, "workspace not found"},
		{"stranger", "team", `{"description":"x"}`, testutil.User(stranger), http.StatusNotFound, "workspace not found"},
		{"member", "team", `{"description":"x"}`, testutil.User(member), http.StatusForbidden, "org members have read-only access"},
		{"unknown slug", "nope", `{"description":"x"}`, o, http.StatusNotFound, "workspace not found"},
		{"empty body", "team", ``, o, http.StatusBadRequest, "invalid request body"},
		{"not an object", "team", `[1]`, o, http.StatusBadRequest, "invalid request body"},
		{"no mutable field", "team", `{}`, o, http.StatusBadRequest, "at least one updatable field"},
		{"immutable slug", "team", `{"slug":"other"}`, o, http.StatusBadRequest, "slug is immutable"},
		{"immutable git_url", "team", `{"git_url":"https://x/y","description":"x"}`, o, http.StatusBadRequest, "git_url is immutable"},
		{"immutable branch", "team", `{"branch":"main"}`, o, http.StatusBadRequest, "branch is immutable"},
		{"immutable owner", "team", `{"owner_id":"me"}`, o, http.StatusBadRequest, "owner_id is immutable"},
		{"immutable mode", "team", `{"workspace_mode":"carry_patch"}`, o, http.StatusBadRequest, "workspace_mode is immutable"},
		{"immutable upstream", "team", `{"upstream_url":"https://u/p"}`, o, http.StatusBadRequest, "upstream_url is immutable"},
		{"immutable integration", "team", `{"integration_branch":"deploy"}`, o, http.StatusBadRequest, "integration_branch is immutable"},
		{"display_name type", "team", `{"display_name":7}`, o, http.StatusBadRequest, "invalid display_name value"},
		{"description type", "team", `{"description":7}`, o, http.StatusBadRequest, "invalid description value"},
		{"org_id type", "team", `{"org_id":7}`, o, http.StatusBadRequest, "invalid org_id value"},
		{"sync_mode type", "team", `{"sync_mode":7}`, o, http.StatusBadRequest, "invalid sync_mode value"},
		{"sync_mode null", "team", `{"sync_mode":null}`, o, http.StatusBadRequest, "invalid sync_mode value"},
		{"sync_mode value", "team", `{"sync_mode":"weekly"}`, o, http.StatusBadRequest, "invalid sync_mode value"},
		{"display_name long", "team", `{"display_name":"` + strings.Repeat("x", 129) + `"}`, o, http.StatusBadRequest, "display_name must not exceed 128"},
		{"description long", "team", `{"description":"` + strings.Repeat("x", 1025) + `"}`, o, http.StatusBadRequest, "description must not exceed 1024"},
		{"settings unknown", "team", `{"settings":{"nope":1}}`, o, http.StatusBadRequest, `unknown setting "nope"`},
		{"org not a member", "team", `{"org_id":"other"}`, o, http.StatusForbidden, "not a member"},
		{"org unknown", "team", `{"org_id":"nope"}`, o, http.StatusBadRequest, "organization not found"},
		{"org service failure", "team", `{"org_id":"flaky"}`, o, http.StatusInternalServerError, "membership check timed out"},
		{"archived", "gone", `{"description":"x"}`, o, http.StatusBadRequest, "must be reactivated before updating"},
	}
	for _, c := range cases {
		if msg := expectStatus(t, patch(t, v, c.slug, c.body, c.auth), c.code); !strings.Contains(msg, c.msg) {
			t.Errorf("%s: %q; want %q", c.name, msg, c.msg)
		}
	}
	if v.column(t, "team", "description") != "" || len(v.events.ofType("hub.workspace.update")) != 0 {
		t.Error("a refused PATCH changed the workspace or emitted an event")
	}
	testutil.Exec(t, v.db, `DROP TABLE workspaces`)
	expectStatus(t, patch(t, v, "team", `{"description":"x"}`, o), http.StatusInternalServerError)
}
