package forge

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// memberID is a member of org O, which holds the workspace widgets of ownerID.
const memberID = "user-member"

func memberKey() *apikit.AuthInfo {
	return &apikit.AuthInfo{UserID: memberID, CredentialType: "api_key"}
}

func memberPAT(perms ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{UserID: memberID, CredentialType: "pat", Permissions: perms}
}

// memberEnv is githubEnv with widgets in org O and memberID its member.
func memberEnv(t *testing.T) *env {
	t.Helper()
	e := githubEnv(t)
	addOrgMember(t, e.db, "widgets", "O", memberID)
	return e
}

// isForbidden reports a 403 forbidden refusal with the read-only message.
func isForbidden(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		return false
	}
	e := decodeError(t, rec)
	return e.ErrorType == "forbidden" && e.Message == "org members have read-only access to this workspace"
}

// dumpRows renders every row of a table.
func dumpRows(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY 1`)
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

// TS-35-40 (forge): readAccess passes a member; writeAccess and
// writeAnyStatusAccess refuse a member with 403 forbidden.
// Requirement: 35-REQ-8.1
func TestForgeAuthorize_Needs_TS_35_40(t *testing.T) {
	e := memberEnv(t)
	h := &handlers{cfg: e.cfg}
	call := func(acc access) (*request, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		c.SetParamNames("slug")
		c.SetParamValues("widgets")
		apikit.SetAuthInfo(c, memberKey())
		req, _ := h.authorize(c, acc)
		return req, rec
	}
	if req, rec := call(readAccess); req == nil || rec.Body.Len() != 0 {
		t.Errorf("member readAccess: %v, %s; want allowed", req, rec.Body)
	}
	for name, acc := range map[string]access{"writeAccess": writeAccess, "writeAnyStatusAccess": writeAnyStatusAccess} {
		if req, rec := call(acc); req != nil || !isForbidden(t, rec) {
			t.Errorf("member %s: %v, %d %s; want 403 forbidden", name, req, rec.Code, rec.Body)
		}
	}
}

// TS-35-43: A member's forge reads succeed with the workspace's credential,
// which reaches the forge but never a response body; the diagnostic names
// only the credential's kind.
// Requirement: 35-REQ-8.4
func TestMemberForgeReads_TS_35_43(t *testing.T) {
	e := memberEnv(t)
	paths := []string{"/workspaces/widgets/forge/deliveries"}
	for _, r := range forgeRoutes {
		paths = append(paths, r.url("widgets"))
	}
	for _, auth := range []*apikit.AuthInfo{memberKey(), memberPAT("forge:read")} {
		for _, path := range paths {
			e.stub.forget()
			owner := e.get(path, apiKeyAuth())
			ownerCalls := e.stub.received()
			e.stub.forget()
			member := e.get(path, auth)
			if member.Code != http.StatusOK || owner.Code != http.StatusOK {
				t.Errorf("%s %s: owner %d, member %d %s; want 200", auth.CredentialType, path, owner.Code, member.Code, member.Body)
				continue
			}
			if strings.Contains(member.Body.String(), "origin-token") {
				t.Errorf("%s %s: the body holds the credential value", auth.CredentialType, path)
			}
			calls := e.stub.received()
			if len(calls) != len(ownerCalls) {
				t.Errorf("%s %s: member made %d forge calls, owner %d", auth.CredentialType, path, len(calls), len(ownerCalls))
			}
			for i, call := range calls {
				if bearer(call) != "origin-token" || bearer(call) != bearer(ownerCalls[i]) {
					t.Errorf("%s %s: forge call %s carried %q; want the workspace credential", auth.CredentialType, path, call.Path, bearer(call))
				}
			}
			if strings.HasSuffix(path, "/forge") && member.Body.String() != owner.Body.String() {
				t.Errorf("diagnostic: member %s, owner %s; want equal", member.Body, owner.Body)
			}
		}
	}
	diag := e.get("/workspaces/widgets/forge", memberKey()).Body.String()
	if !strings.Contains(diag, `"credential":"pat"`) {
		t.Errorf("diagnostic %s: credential is not only its kind", diag)
	}
}

// TS-35-44: A member's forge writes and webhook changes are refused with 403
// forbidden, with no forge request and the webhook configuration unchanged.
// Requirement: 35-REQ-8.5
func TestMemberForgeWrites_TS_35_44(t *testing.T) {
	e := memberEnv(t)
	if _, err := e.secrets.CreateSecrets("workspace", "widgets", []secrets.EntryInput{{Key: "FORGE_WEBHOOK_SECRET", Value: "s3cret-hook"}}); err != nil {
		t.Fatal(err)
	}
	before := dumpRows(t, e.db, "secrets")
	e.stub.forget()
	writes := append([]writeRouteCase{}, writeRouteCases...)
	writes = append(writes,
		writeRouteCase{"set webhook", http.MethodPut, "/workspaces/%s/forge/webhook", `{}`},
		writeRouteCase{"unset webhook", http.MethodDelete, "/workspaces/%s/forge/webhook", ``})
	for _, auth := range []*apikit.AuthInfo{memberKey(), memberPAT("forge:read", "forge:write")} {
		for _, w := range writes {
			if rec := e.sendRoute(w, "widgets", auth); !isForbidden(t, rec) {
				t.Errorf("%s %s: %d %s; want 403 forbidden", auth.CredentialType, w.name, rec.Code, rec.Body)
			}
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge saw %d requests; want 0", n)
	}
	if after := dumpRows(t, e.db, "secrets"); after != before {
		t.Errorf("webhook configuration changed:\nbefore %s\nafter %s", before, after)
	}
}
