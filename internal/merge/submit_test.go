package merge

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// A valid submission enqueues one merge job keyed by workspace, target and
// source, grouped by workspace and target, attributed to the caller (the
// user of an API key or PAT, "admin" for an admin token), answers 202 with
// the queued job and emits hub.merge.enqueue; the same submission again is a
// 409 duplicate.
func TestSubmit_EnqueuesMergeJob(t *testing.T) {
	v := newEnv(t)
	for _, c := range []struct {
		auth        *apikit.AuthInfo
		source      string
		submittedBy string
	}{
		{testutil.User(owner), "feature/a", owner},
		{testutil.Admin(), "feature/b", "admin"},
		{testutil.PAT(owner, "merges:write"), "feature/c", owner},
	} {
		v.branches[ws+":"+c.source] = true
		rec := v.do(t, http.MethodPost, merges, `{"target_branch":"main","source_ref":"`+c.source+`"}`, c.auth)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: %d %s", c.auth.CredentialType, rec.Code, rec.Body)
		}
		got := testutil.Object(t, rec)
		if got["status"] != "queued" || got["workspace_slug"] != ws || got["target_branch"] != "main" || got["source_ref"] != c.source ||
			got["submitted_by"] != c.submittedBy || got["base_sha"] != nil || got["error"] != nil || len(got["conflict_files"].([]any)) != 0 || got["id"] == "" {
			t.Errorf("%s: body %v", c.auth.CredentialType, got)
		}
		job, err := v.queue.GetByID(got["id"].(string))
		if err != nil || job.Key != ws+":main:"+c.source || job.GroupKey != ws+":main" || job.SubmittedBy != c.submittedBy || job.Type != MergeJobType {
			t.Errorf("%s: job %+v, %v", c.auth.CredentialType, job, err)
		}
		if !strings.Contains(string(job.Payload), `"submitted_by":"`+c.submittedBy+`"`) {
			t.Errorf("%s: payload %s", c.auth.CredentialType, job.Payload)
		}
	}
	rec := v.do(t, http.MethodPost, merges, `{"target_branch":"main","source_ref":"feature/a"}`, testutil.User(owner))
	wantError(t, rec, http.StatusConflict, "a merge job for this source and target branch is already queued or running", "duplicate_merge")
	evs := v.audit.all()
	if len(evs) != 3 || evs[0].EventType != "hub.merge.enqueue" || evs[0].ResourceType != "merge" || evs[0].Action != "enqueue" || evs[0].Workspace != ws ||
		evs[0].ActorID != owner || evs[0].ActorType != "api_key" || evs[0].Metadata["source_ref"] != "feature/a" || evs[0].Metadata["job_id"] != evs[0].ResourceID ||
		evs[1].ActorType != "admin_token" {
		t.Errorf("events %+v", evs)
	}
}

// The submit refusals, in the order the handler checks: credential, PAT
// scope, body, ref names, workspace access and state, branches.
func TestSubmit_Refusals(t *testing.T) {
	v := newEnv(t)
	testutil.Exec(t, v.db, `INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, created_at, updated_at) VALUES
		('archived', 'u', 'owner', 'archived', 'ready', '', ''), ('cloning', 'u', 'owner', 'active', 'cloning', '', '')`)
	valid := `{"target_branch":"main","source_ref":"feature/a"}`
	cases := []struct {
		name, slug, body string
		auth             *apikit.AuthInfo
		status           int
		message, typ     string
	}{
		{"no credential", ws, valid, nil, http.StatusUnauthorized, "authentication required", ""},
		{"PAT without scope", ws, valid, testutil.PAT(owner, "merges:read"), http.StatusForbidden, "PAT requires merges:write scope", ""},
		{"malformed body", ws, `{"target_branch":`, testutil.User(owner), http.StatusBadRequest, "invalid request body: unexpected EOF", ""},
		{"no target", ws, `{"source_ref":"feature/a"}`, testutil.User(owner), http.StatusBadRequest, "target_branch is required", ""},
		{"no source", ws, `{"target_branch":"main"}`, testutil.User(owner), http.StatusBadRequest, "source_ref is required", ""},
		{"option-like target", ws, `{"target_branch":"--upload-pack=x","source_ref":"feature/a"}`, testutil.User(owner), http.StatusBadRequest, "invalid target_branch: ref name must not start with '-'", ""},
		{"option-like source", ws, `{"target_branch":"main","source_ref":"-x"}`, testutil.User(owner), http.StatusBadRequest, "invalid source_ref: ref name must not start with '-'", ""},
		{"unknown workspace", "ghost", valid, testutil.User(owner), http.StatusNotFound, "workspace not found", ""},
		{"stranger", ws, valid, testutil.User(stranger), http.StatusNotFound, "workspace not found", ""},
		{"member", ws, valid, testutil.User(member), http.StatusForbidden, "org members have read-only access to this workspace", wsaccess.ErrorTypeForbidden},
		{"archived", "archived", valid, testutil.User(owner), http.StatusBadRequest, "workspace is not active", ""},
		{"clone not ready", "cloning", valid, testutil.User(owner), http.StatusBadRequest, "workspace clone is not ready", ""},
		{"unknown target", ws, `{"target_branch":"nope","source_ref":"feature/a"}`, testutil.User(owner), http.StatusBadRequest, `branch "nope" does not exist in workspace`, ""},
		{"unknown source", ws, `{"target_branch":"main","source_ref":"nope"}`, testutil.User(owner), http.StatusBadRequest, `branch "nope" does not exist in workspace`, ""},
		{"branch check fails", ws, `{"target_branch":"main","source_ref":"broken"}`, testutil.User(owner), http.StatusInternalServerError, "internal server error", ""},
	}
	for _, c := range cases {
		rec := v.do(t, http.MethodPost, "/api/v1/workspaces/"+c.slug+"/merges", c.body, c.auth)
		if rec.Code != c.status || testutil.Error(t, rec).Message != c.message || testutil.Error(t, rec).ErrorType != c.typ {
			t.Errorf("%s: %d %s; want %d %q", c.name, rec.Code, rec.Body, c.status, c.message)
		}
	}
	if n := testutil.Count(t, v.db, `SELECT COUNT(*) FROM jobs`); n != 0 || len(v.audit.all()) != 0 {
		t.Errorf("%d jobs and %d events after the refusals", n, len(v.audit.all()))
	}
}

// An emitter that fails does not change the answer; without an emitter
// nothing is emitted.
func TestSubmit_AuditIsOptionalAndNonBlocking(t *testing.T) {
	v := newEnv(t)
	v.audit.err = context.DeadlineExceeded
	if rec := v.do(t, http.MethodPost, merges, `{"target_branch":"main","source_ref":"feature/a"}`, testutil.User(owner)); rec.Code != http.StatusAccepted {
		t.Errorf("with a failing emitter: %d %s", rec.Code, rec.Body)
	}
	e, api := testutil.API()
	RegisterMergeRoutes(api, MergeAPIConfig{DB: v.db, Queue: v.queue})
	v.branches[ws+":feature/b"] = true
	rec := testutil.Request(t, e, http.MethodPost, merges, `{"target_branch":"main","source_ref":"feature/b"}`, testutil.User(owner))
	if rec.Code != http.StatusAccepted || len(v.audit.all()) != 1 {
		t.Errorf("without an emitter: %d %s, %d events", rec.Code, rec.Body, len(v.audit.all()))
	}
}

// Both merge scopes are registered with apikit.
func TestMergePermissions(t *testing.T) {
	p := MergePermissions()
	if len(p) != 2 || p[0] != (apikit.Permission{Resource: "merges", Action: "read"}) || p[1] != (apikit.Permission{Resource: "merges", Action: "write"}) {
		t.Errorf("MergePermissions() = %v", p)
	}
}
