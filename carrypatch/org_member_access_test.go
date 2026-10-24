package carrypatch

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// Spec 35 fixture: owner-1 owns the carry-patch workspace "cp" in org O,
// whose member is member-2; stranger-3 belongs to no org.
const (
	cpOwner    = "owner-1"
	cpMember   = "member-2"
	cpStranger = "stranger-3"
)

// carryPatchScopes are the PAT scopes every carry-patch route needs.
var carryPatchScopes = []string{"rebuilds:read", "rebuilds:write", "workspaces:read", "workspaces:write", "workspaces:write"}

// newMemberCarryPatchEnv is newFullTestEnv with workspace cp, a queued
// rebuild job "job-1" and two rr-cache entries.
func newMemberCarryPatchEnv(t *testing.T) *fullTestEnv {
	t.Helper()
	env := newFullTestEnv(t)
	seedWorkspaceCarryPatch(t, env.db, "cp", cpOwner, "https://github.com/upstream/repo", "abc123", "deploy", "")
	addOrgMember(t, env.db, "cp", "O", cpMember)
	addOrgMember(t, env.db, "cp", "O", cpOwner)
	seedRebuildJob(t, env.db, "job-1", "queued", "cp", "rebase", cpOwner)
	setupRRCacheDir(t, env.workspaceRoot, "cp", []rrCacheEntry{{hash: "h1", preimage: "a", postimage: "b"}, {hash: "h2", preimage: "c"}})
	return env
}

// forbiddenBody checks a 403 forbidden refusal from WriteRefusal.
func forbiddenBody(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Error struct {
			Message   string `json:"message"`
			ErrorType string `json:"error_type"`
		} `json:"error"`
	}
	if rec.Code != http.StatusForbidden || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		body.Error.ErrorType != "forbidden" || body.Error.Message != "org members have read-only access to this workspace" {
		t.Errorf("%s: %d %s; want 403 with error_type forbidden and the read-only message", name, rec.Code, rec.Body)
	}
}

// dumpJobs renders the jobs table.
func dumpJobs(t *testing.T, db *sql.DB) string {
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

// listTree lists every path under dir.
func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(path string, _ os.FileInfo, err error) error {
		if err == nil {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// TS-35-36: authorizeWorkspace passes a member on read, refuses a member's
// write with 403 forbidden and a stranger with 404 without error_type.
// Requirement: 35-REQ-7.1
func TestAuthorizeWorkspace_Needs_TS_35_36(t *testing.T) {
	env := newMemberCarryPatchEnv(t)
	call := func(user string, need Need) (bool, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		return authorizeWorkspace(c, env.db, &apikit.AuthInfo{CredentialType: "api_key", UserID: user}, "cp", need), rec
	}
	if ok, rec := call(cpMember, NeedRead); !ok || rec.Body.Len() != 0 {
		t.Errorf("member read: %v, %s; want allowed, nothing written", ok, rec.Body)
	}
	if ok, rec := call(cpOwner, NeedWrite); !ok || rec.Body.Len() != 0 {
		t.Errorf("owner write: %v, %s; want allowed, nothing written", ok, rec.Body)
	}
	if ok, rec := call(cpMember, NeedWrite); ok {
		t.Error("member write allowed")
	} else {
		forbiddenBody(t, "member write", rec)
	}
	for _, need := range []Need{NeedRead, NeedWrite} {
		ok, rec := call(cpStranger, need)
		if ok || rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "error_type") ||
			parseErrorEnvelope(t, rec).Error.Message != "workspace not found" {
			t.Errorf("stranger %s: %v, %d %s; want 404 workspace not found without error_type", need, ok, rec.Code, rec.Body)
		}
	}
}

// TS-35-37 (carry-patch): A member reads rebuilds, a rebuild, the preview,
// patch-status and rerere with the owner's bodies.
// Requirement: 35-REQ-7.2
func TestMemberCarryPatchReads_TS_35_37(t *testing.T) {
	env := newMemberCarryPatchEnv(t)
	for _, path := range []string{
		"/api/v1/workspaces/cp/rebuilds", "/api/v1/workspaces/cp/rebuilds/job-1", "/api/v1/workspaces/cp/rebuild-preview",
		"/api/v1/workspaces/cp/patch-status", "/api/v1/workspaces/cp/rerere",
	} {
		owner := env.doRequest(t, http.MethodGet, path, "", rebuildPATAuth(cpOwner, carryPatchScopes...))
		member := env.doRequest(t, http.MethodGet, path, "", rebuildPATAuth(cpMember, carryPatchScopes...))
		if owner.Code != http.StatusOK || member.Code != http.StatusOK || owner.Body.String() != member.Body.String() {
			t.Errorf("GET %s: owner %d %s, member %d %s; want 200 and equal bodies", path, owner.Code, owner.Body, member.Code, member.Body)
		}
	}
}

// TS-35-38 (carry-patch): A member's rebuild and rerere writes are refused
// with 403 forbidden; the job queue and the rerere cache are unchanged.
// Requirement: 35-REQ-7.3
func TestMemberCarryPatchWrites_TS_35_38(t *testing.T) {
	env := newMemberCarryPatchEnv(t)
	jobs := dumpJobs(t, env.db)
	cache := listTree(t, filepath.Join(env.workspaceRoot, "cp"))
	for _, auth := range []*apikit.AuthInfo{rebuildUserAuth(cpMember), rebuildPATAuth(cpMember, carryPatchScopes...)} {
		for _, w := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/v1/workspaces/cp/rebuild", `{}`},
			{http.MethodDelete, "/api/v1/workspaces/cp/rebuilds/job-1", ""},
			{http.MethodPost, "/api/v1/workspaces/cp/rebuilds/job-1/requeue", ""},
			{http.MethodPost, "/api/v1/workspaces/cp/rebuilds/job-1/rollback", ""},
			{http.MethodDelete, "/api/v1/workspaces/cp/rerere/h1", ""},
		} {
			forbiddenBody(t, auth.CredentialType+" "+w.method+" "+w.path, env.doRequest(t, w.method, w.path, w.body, auth))
		}
	}
	if got := dumpJobs(t, env.db); got != jobs {
		t.Errorf("jobs changed:\nbefore %s\nafter %s", jobs, got)
	}
	if got := listTree(t, filepath.Join(env.workspaceRoot, "cp")); strings.Join(got, "\n") != strings.Join(cache, "\n") {
		t.Errorf("rerere cache changed: before %v, after %v", cache, got)
	}
}

// TS-35-39: runCarryPatchSync authorizes with NeedWrite: a member gets 403
// forbidden through the sync route, the owner gets today's answer.
// Requirement: 35-REQ-7.4
func TestCarryPatchSync_NeedWrite_TS_35_39(t *testing.T) {
	env := newMemberCarryPatchEnv(t)
	jobs := dumpJobs(t, env.db)
	forbiddenBody(t, "member sync", env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cp/sync", "", rebuildUserAuth(cpMember)))
	if got := dumpJobs(t, env.db); got != jobs {
		t.Errorf("member sync changed jobs:\nbefore %s\nafter %s", jobs, got)
	}
	owner := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cp/sync", "", rebuildUserAuth(cpOwner))
	if owner.Code != http.StatusOK {
		t.Errorf("owner sync: %d %s; want 200", owner.Code, owner.Body)
	}
}
