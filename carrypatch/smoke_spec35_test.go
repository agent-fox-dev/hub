package carrypatch

import (
	"net/http"
	"testing"
)

// TS-35-58 (smoke): A member's rebuild request is refused with 403 forbidden
// and nothing is enqueued, while the owner's same request is.
// Path: 35-PATH-2. Requirement: 35-REQ-7.3
func TestSmoke_MemberRebuildRefused_TS_35_58(t *testing.T) {
	env := newRebuildTestEnv(t)
	seedWorkspace(t, env.db, "cp", cpOwner, "ready", "carry_patch", "deploy")
	seedPatch(t, env.db, "p1", "cp", "feature/a", 1, "active")
	addOrgMember(t, env.db, "cp", "O", cpMember)
	jobs := func() int {
		var n int
		if err := env.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cp/rebuild", `{}`, rebuildPATAuth(cpMember, "rebuilds:write"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member rebuild: %d %s; want 403", rec.Code, rec.Body)
	}
	e := parseErrorEnvelope(t, rec)
	if e.Error.Message != "org members have read-only access to this workspace" || e.Error.ErrorType != "forbidden" {
		t.Errorf("member rebuild body %+v; want the read-only message with error_type forbidden", e)
	}
	if n := jobs(); n != 0 {
		t.Errorf("jobs after the member's request = %d; want 0", n)
	}

	if rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cp/rebuild", `{}`, rebuildPATAuth(cpOwner, "rebuilds:write")); rec.Code != http.StatusAccepted {
		t.Fatalf("owner rebuild: %d %s; want 202", rec.Code, rec.Body)
	}
	if n := jobs(); n != 1 {
		t.Errorf("jobs after the owner's request = %d; want 1", n)
	}
}
