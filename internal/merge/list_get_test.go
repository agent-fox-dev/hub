package merge

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/aflib/jobqueue"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// The list is the workspace's merge jobs, [] when it has none; the owner,
// an admin token, a member of the org and a PAT with merges:read (or
// merges:write) read it; a stranger gets the unknown workspace's 404.
func TestList_WorkspaceJobs(t *testing.T) {
	v := newEnv(t)
	v.seedJob(t, "j1", "queued", ws, "main", "feature/a", "", "")
	v.seedJob(t, "j2", "completed", ws, "main", "feature/b", `{"base_sha":"b","merged_sha":"m"}`, "")
	v.seedJob(t, "other", "queued", "other-ws", "main", "feature/a", "", "")
	testutil.Exec(t, v.db, `INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, created_at, updated_at) VALUES ('empty', 'u', 'owner', 'active', 'ready', '', '')`)
	for _, c := range []struct {
		slug string
		auth *apikit.AuthInfo
		code int
		ids  string
	}{
		{ws, testutil.User(owner), http.StatusOK, "j1 j2"},
		{ws, testutil.Admin(), http.StatusOK, "j1 j2"},
		{ws, testutil.User(member), http.StatusOK, "j1 j2"},
		{ws, testutil.PAT(member, "merges:read"), http.StatusOK, "j1 j2"},
		{ws, testutil.PAT(owner, "merges:write"), http.StatusOK, "j1 j2"},
		{"empty", testutil.User(owner), http.StatusOK, ""},
		{ws, testutil.PAT(owner, "workspaces:read"), http.StatusForbidden, ""},
		{ws, testutil.User(stranger), http.StatusNotFound, ""},
		{"ghost", testutil.User(owner), http.StatusNotFound, ""},
		{ws, nil, http.StatusUnauthorized, ""},
	} {
		rec := v.do(t, http.MethodGet, "/api/v1/workspaces/"+c.slug+"/merges", "", c.auth)
		if rec.Code != c.code {
			t.Errorf("%s as %+v: %d %s, want %d", c.slug, c.auth, rec.Code, rec.Body, c.code)
			continue
		}
		if c.code != http.StatusOK {
			continue
		}
		var ids []string
		for _, j := range testutil.Objects(t, rec) {
			ids = append(ids, j["id"].(string))
		}
		sort.Strings(ids)
		if strings.Join(ids, " ") != c.ids || (c.ids == "" && strings.TrimSpace(rec.Body.String()) != "[]") {
			t.Errorf("%s: %s, want ids %q", c.slug, rec.Body, c.ids)
		}
	}
}

// Get answers the job's record; a job of another workspace, a job of
// another type and an unknown id are 404, and a PAT needs merges:read.
func TestGet_JobOfTheWorkspace(t *testing.T) {
	v := newEnv(t)
	v.seedJob(t, "j1", "failed", ws, "main", "feature/a", "", `{"reason":"WouldConflict","conflict_files":["a.txt"]}`)
	v.seedJob(t, "other", "queued", "other-ws", "main", "feature/a", "", "")
	testutil.Exec(t, v.db, `UPDATE jobs SET type = 'sync', key = 'ws:x' WHERE id = 'other'`)
	rec := v.do(t, http.MethodGet, merges+"/j1", "", testutil.PAT(member, "merges:read"))
	if got := testutil.Object(t, rec); rec.Code != http.StatusOK || got["id"] != "j1" || got["status"] != "failed" || got["conflict_files"].([]any)[0] != "a.txt" {
		t.Errorf("get: %d %s", rec.Code, rec.Body)
	}
	for _, id := range []string{"other", "ghost"} {
		wantError(t, v.do(t, http.MethodGet, merges+"/"+id, "", testutil.User(owner)), http.StatusNotFound, "merge job not found", "")
	}
	wantError(t, v.do(t, http.MethodGet, merges+"/j1", "", testutil.PAT(owner, "workspaces:read")), http.StatusForbidden, "PAT requires merges:read scope", "")
	wantError(t, v.do(t, http.MethodGet, merges+"/j1", "", testutil.User(stranger)), http.StatusNotFound, "workspace not found", "")
}

// A job record is projected into the merge vocabulary: the payload's
// branches and submitter, the result's SHAs (null until there are any), the
// conflict files of a structured error ([] otherwise) and the raw error.
func TestProjectMergeJobResponse(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	payload := json.RawMessage(`{"workspace_slug":"ws","target_branch":"main","source_ref":"feature/a","submitted_by":"alice"}`)
	cases := []struct {
		name string
		job  jobqueue.Job
		want string
	}{
		{"queued", jobqueue.Job{ID: "j", Status: "queued", Payload: payload, RetryCount: 1, CreatedAt: now, UpdatedAt: now},
			`{"id":"j","workspace_slug":"ws","target_branch":"main","source_ref":"feature/a","status":"queued","base_sha":null,"merged_sha":null,"conflict_files":[],"error":null,"retry_count":1,"submitted_by":"alice","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`},
		{"completed", jobqueue.Job{ID: "j", Status: "completed", Payload: payload, Result: json.RawMessage(`{"base_sha":"b1","merged_sha":"m1"}`), CreatedAt: now, UpdatedAt: now},
			`"base_sha":"b1","merged_sha":"m1","conflict_files":[],"error":null`},
		{"conflict", jobqueue.Job{ID: "j", Status: "failed", Payload: payload, Error: `{"reason":"WouldConflict","conflict_files":["a.txt","b.txt"]}`, CreatedAt: now, UpdatedAt: now},
			`"conflict_files":["a.txt","b.txt"],"error":"{\"reason\":\"WouldConflict\",\"conflict_files\":[\"a.txt\",\"b.txt\"]}"`},
		{"plain error", jobqueue.Job{ID: "j", Status: "dead_letter", Payload: json.RawMessage(`not json`), Error: "fetch failed", CreatedAt: now, UpdatedAt: now},
			`"workspace_slug":"","target_branch":"","source_ref":"","status":"dead_letter","base_sha":null,"merged_sha":null,"conflict_files":[],"error":"fetch failed"`},
	}
	for _, c := range cases {
		b, _ := json.Marshal(ProjectMergeJobResponse(&c.job))
		if !strings.Contains(string(b), c.want) {
			t.Errorf("%s: %s\nwant it to contain %s", c.name, b, c.want)
		}
	}
}
