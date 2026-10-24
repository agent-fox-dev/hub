package merge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// Spec 35 fixture: owner-1 owns workspace "mw" in org O, whose member is
// member-2; stranger-3 belongs to no org.
const (
	mOwner    = "owner-1"
	mMember   = "member-2"
	mStranger = "stranger-3"
)

var mergeScopes = []string{"merges:read", "merges:write", "workspaces:write"}

// newMemberMergeEnv is newMergeTestEnvWithRebase with workspace mw, its
// branches, a queued merge job "m-1", and a rebase function that counts calls.
func newMemberMergeEnv(t *testing.T) (*mergeTestEnv, *atomic.Int32) {
	t.Helper()
	var rebases atomic.Int32
	env := newMergeTestEnvWithRebase(t, map[string]bool{"mw:main": true, "mw:feature": true},
		func(context.Context, string, string, []string) ([]RebaseResult, error) {
			rebases.Add(1)
			return nil, nil
		})
	seedTestWorkspace(t, env.db, "mw", mOwner, "ready")
	addOrgMember(t, env.db, "mw", "O", mMember)
	seedMergeJob(t, env.db, "m-1", "queued", "mw", "main", "feature", mOwner, nil, nil)
	return env, &rebases
}

// dumpMergeJobs renders the jobs table.
func dumpMergeJobs(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var b strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%v\n", vals)
	}
	return b.String()
}

// isForbidden reports whether rec is a 403 forbidden refusal from WriteRefusal.
func isForbidden(rec *httptest.ResponseRecorder) bool {
	var body struct {
		Error struct {
			Message   string `json:"message"`
			ErrorType string `json:"error_type"`
		} `json:"error"`
	}
	return rec.Code == http.StatusForbidden && json.Unmarshal(rec.Body.Bytes(), &body) == nil &&
		body.Error.ErrorType == "forbidden" && body.Error.Message == "org members have read-only access to this workspace"
}

// TS-35-37 (merge): A member lists and gets merges with the owner's bodies.
// Requirement: 35-REQ-7.2
func TestMemberMergeReads_TS_35_37(t *testing.T) {
	env, _ := newMemberMergeEnv(t)
	for _, path := range []string{"/api/v1/workspaces/mw/merges", "/api/v1/workspaces/mw/merges/m-1"} {
		owner := env.doRequest(t, http.MethodGet, path, "", mergePATAuth(mOwner, mergeScopes...))
		member := env.doRequest(t, http.MethodGet, path, "", mergePATAuth(mMember, mergeScopes...))
		if owner.Code != http.StatusOK || member.Code != http.StatusOK || owner.Body.String() != member.Body.String() {
			t.Errorf("GET %s: owner %d %s, member %d %s; want 200 and equal bodies", path, owner.Code, owner.Body, member.Code, member.Body)
		}
	}
}

// TS-35-38 (merge): A member's merge submit, cancel, requeue and rebase are
// refused with 403 forbidden; nothing is enqueued, cancelled or rebased.
// Requirement: 35-REQ-7.3
func TestMemberMergeWrites_TS_35_38(t *testing.T) {
	env, rebases := newMemberMergeEnv(t)
	jobs := dumpMergeJobs(t, env.db)
	for _, auth := range []*apikit.AuthInfo{mergeUserAuth(mMember), mergePATAuth(mMember, mergeScopes...)} {
		for _, w := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/v1/workspaces/mw/merges", `{"target_branch":"main","source_ref":"feature"}`},
			{http.MethodDelete, "/api/v1/workspaces/mw/merges/m-1", ""},
			{http.MethodPost, "/api/v1/workspaces/mw/merges/m-1/requeue", ""},
			{http.MethodPost, "/api/v1/workspaces/mw/rebase", `{"target_ref":"main","branches":["feature"]}`},
		} {
			if rec := env.doRequest(t, w.method, w.path, w.body, auth); !isForbidden(rec) {
				t.Errorf("%s %s %s: %d %s; want 403 forbidden", auth.CredentialType, w.method, w.path, rec.Code, rec.Body)
			}
		}
	}
	if got := dumpMergeJobs(t, env.db); got != jobs {
		t.Errorf("jobs changed:\nbefore %s\nafter %s", jobs, got)
	}
	if n := rebases.Load(); n != 0 {
		t.Errorf("batch rebase ran %d times; want 0", n)
	}
}

// TS-35-36 (merge): authorizeMergeWorkspace and validateWorkspaceForMerge
// pass a member on read, refuse a member's write with 403 forbidden and a
// stranger with 404 without error_type, and report every refusal as false.
// Requirement: 35-REQ-7.1
func TestMergeHelpers_Needs_TS_35_36(t *testing.T) {
	env, _ := newMemberMergeEnv(t)
	ctx := func() (echo.Context, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		return echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec), rec
	}
	user := mergeUserAuth

	c, rec := ctx()
	if !authorizeMergeWorkspace(c, env.db, user(mMember), "mw", wsaccess.NeedRead) || rec.Body.Len() != 0 {
		t.Errorf("member read refused: %s", rec.Body)
	}
	c, rec = ctx()
	if authorizeMergeWorkspace(c, env.db, user(mMember), "mw", wsaccess.NeedWrite) || !isForbidden(rec) {
		t.Errorf("member write: %d %s; want false and 403 forbidden", rec.Code, rec.Body)
	}
	c, rec = ctx()
	if validateWorkspaceForMerge(c, env.db, user(mMember), "mw") || !isForbidden(rec) {
		t.Errorf("validateWorkspaceForMerge(member): %d %s; want false and 403 forbidden", rec.Code, rec.Body)
	}
	c, rec = ctx()
	if !validateWorkspaceForMerge(c, env.db, user(mOwner), "mw") || rec.Body.Len() != 0 {
		t.Errorf("validateWorkspaceForMerge(owner) refused: %s", rec.Body)
	}
	for _, check := range []func(echo.Context) bool{
		func(c echo.Context) bool {
			return authorizeMergeWorkspace(c, env.db, user(mStranger), "mw", wsaccess.NeedRead)
		},
		func(c echo.Context) bool {
			return authorizeMergeWorkspace(c, env.db, user(mStranger), "mw", wsaccess.NeedWrite)
		},
		func(c echo.Context) bool { return validateWorkspaceForMerge(c, env.db, user(mStranger), "mw") },
	} {
		c, rec := ctx()
		if check(c) || rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "error_type") ||
			parseMergeErrorEnvelope(t, rec).Error.Message != "workspace not found" {
			t.Errorf("stranger: %d %s; want false and 404 workspace not found without error_type", rec.Code, rec.Body)
		}
	}
}
