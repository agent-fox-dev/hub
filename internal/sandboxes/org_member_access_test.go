package sandboxes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Spec 35 fixture: leaseOwner owns workspace "team" in org O, whose member is
// member-2.
const leaseMember = "member-2"

func memberKey() *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "api_key", UserID: leaseMember, KeyID: "key-2"}
}

func memberPAT(perms ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "pat", UserID: leaseMember, KeyID: "pat-2", Permissions: perms}
}

// addTeam adds workspace "team" of leaseOwner in org O with GIT_PAT planted,
// and makes leaseMember a member of O.
func addTeam(t *testing.T, e *leaseEnv) {
	t.Helper()
	e.addWorkspace("team", leaseOwner, "O", "active")
	e.plantSecret("workspace", "team", "GIT_PAT", "TEAM-VALUE-456")
	if _, err := e.db.Exec(`INSERT INTO org_members (org_id, user_id) VALUES ('O', ?)`, leaseMember); err != nil {
		t.Fatal(err)
	}
}

// issueTeam issues a lease of profile tools in team as the owner.
func issueTeam(t *testing.T, e *leaseEnv) string {
	t.Helper()
	rec := e.post("team", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner POST team: %d %s", rec.Code, rec.Body)
	}
	return jsonObject(t, rec)["id"].(string)
}

// assertMemberForbidden checks a 403 forbidden refusal.
func assertMemberForbidden(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden || errorType(t, rec) != "forbidden" || message(t, rec) != "org members have read-only access to this workspace" {
		t.Errorf("%s: %d %s; want 403 forbidden with the read-only message", name, rec.Code, rec.Body)
	}
}

// TS-35-40 (leases): authorize(c, false) passes a member, authorize(c, true)
// refuses a member with 403 forbidden.
// Requirement: 35-REQ-8.1
func TestLeaseAuthorize_Needs_TS_35_40(t *testing.T) {
	e := newLeaseEnv(t, leaseEnvOptions{})
	addTeam(t, e)
	call := func(write bool) (*leaseRequest, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		c.SetParamNames("slug")
		c.SetParamValues("team")
		apikit.SetAuthInfo(c, memberKey())
		req, _ := e.svc.authorize(c, write)
		return req, rec
	}
	if req, rec := call(false); req == nil || rec.Body.Len() != 0 {
		t.Errorf("member read: %v, %s; want allowed", req, rec.Body)
	}
	if req, rec := call(true); req != nil {
		t.Error("member write allowed")
	} else {
		assertMemberForbidden(t, "member write", rec)
	}
}

// TS-35-41: A member lists and gets leases with the owner's bodies, the
// secrets and optional_secrets names unredacted.
// Requirement: 35-REQ-8.2
func TestMemberLeaseReads_TS_35_41(t *testing.T) {
	e := newLeaseEnv(t, leaseEnvOptions{})
	addTeam(t, e)
	id := issueTeam(t, e)
	for _, path := range []string{"/api/v1/workspaces/team/sandboxes", "/api/v1/workspaces/team/sandboxes/" + id} {
		owner := e.do(http.MethodGet, path, "", apiKeyAuth())
		for _, auth := range []*apikit.AuthInfo{memberKey(), memberPAT("sandboxes:read")} {
			member := e.do(http.MethodGet, path, "", auth)
			if owner.Code != http.StatusOK || member.Code != http.StatusOK || owner.Body.String() != member.Body.String() {
				t.Errorf("GET %s: owner %d %s, member %d %s; want 200 and equal bodies", path, owner.Code, owner.Body, member.Code, member.Body)
			}
		}
		if !strings.Contains(owner.Body.String(), `"GIT_PAT"`) || !strings.Contains(owner.Body.String(), `"EXTRA_TOKEN"`) {
			t.Errorf("GET %s: body %s lacks the secret names", path, owner.Body)
		}
	}
}

// TS-35-42: A member cannot issue or delete a lease; no lease row is created
// or ended and the backend is not called.
// Requirement: 35-REQ-8.3
func TestMemberLeaseWrites_TS_35_42(t *testing.T) {
	e := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	addTeam(t, e.leaseEnv)
	id := issueTeam(t, e.leaseEnv)
	if _, err := e.create(id, sandbox.Spec{Image: "registry.example/tools:1"}); err != nil {
		t.Fatalf("creating the lease's sandbox: %v", err)
	}
	leases, row, creates, live := e.countLeases(), strings.Join(e.rowText(id), "|"), e.prov.creates(), strings.Join(e.prov.live(), ",")

	for _, auth := range []*apikit.AuthInfo{memberKey(), memberPAT("sandboxes:read", "sandboxes:write")} {
		assertMemberForbidden(t, auth.CredentialType+" POST", e.post("team", `{"profile":"tools"}`, auth))
		assertMemberForbidden(t, auth.CredentialType+" DELETE", e.do(http.MethodDelete, "/api/v1/workspaces/team/sandboxes/"+id, "", auth))
	}
	if e.countLeases() != leases || strings.Join(e.rowText(id), "|") != row {
		t.Errorf("lease rows changed: %d -> %d, %s -> %v", leases, e.countLeases(), row, e.rowText(id))
	}
	if e.prov.creates() != creates || strings.Join(e.prov.live(), ",") != live {
		t.Errorf("backend called: creates %d -> %d, live %s -> %v", creates, e.prov.creates(), live, e.prov.live())
	}
}
