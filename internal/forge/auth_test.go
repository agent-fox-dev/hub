package forge

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// TS-30-4: forge.Permissions returns exactly forge:read and forge:write.
// Requirement: 30-REQ-2.1
func TestPermissions_TS_30_4(t *testing.T) {
	want := []apikit.Permission{
		{Resource: "forge", Action: "read"},
		{Resource: "forge", Action: "write"},
	}
	if got := Permissions(); !reflect.DeepEqual(got, want) {
		t.Errorf("Permissions() = %v; want %v", got, want)
	}
}

// TS-30-5: A request without a credential gets 401 on every forge route before any
// lookup.
// Requirement: 30-REQ-2.2
func TestNoCredentialGets401_TS_30_5(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "widgets")
	// With the database closed, a lookup of the workspace would answer 500: a 401
	// shows the table was not queried.
	e.db.Close()

	for _, r := range forgeRoutes {
		t.Run(r.name, func(t *testing.T) {
			rec := e.get(r.url("widgets"), nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d; want 401\n%s", rec.Code, rec.Body)
			}
			decodeError(t, rec)
		})
	}
	assertNothingLookedUp(t, e)
}

// TS-30-6: A PAT with neither forge scope gets 403 naming forge:read on every route,
// also for a slug that does not exist.
// Requirement: 30-REQ-2.3
func TestPATWithoutForgeScopeGets403_TS_30_6(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "widgets")
	pat := patAuth("sandboxes:read")

	for _, r := range forgeRoutes {
		for _, slug := range []string{"widgets", "missing"} {
			t.Run(r.name+"/"+slug, func(t *testing.T) {
				rec := e.get(r.url(slug), pat)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d; want 403\n%s", rec.Code, rec.Body)
				}
				if msg := decodeError(t, rec).Message; !strings.Contains(msg, "forge:read") {
					t.Errorf("message %q does not name forge:read", msg)
				}
			})
		}
	}
	assertNothingLookedUp(t, e)
}

// TS-30-8: A credential type hub does not know is denied and nothing is sent to the
// forge.
// Requirement: 30-REQ-2.5
func TestUnknownCredentialTypeIsDenied_TS_30_8(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "widgets")
	mystery := &apikit.AuthInfo{UserID: ownerID, CredentialType: "mystery", Permissions: []string{"forge:read", "forge:write"}}

	for _, r := range forgeRoutes {
		t.Run(r.name, func(t *testing.T) {
			rec := e.get(r.url("widgets"), mystery)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d; want 403\n%s", rec.Code, rec.Body)
			}
			decodeError(t, rec)
		})
	}
	assertNothingLookedUp(t, e)
}

// TS-30-9: A missing or foreign workspace gives 404 on every route, without a
// credential being resolved and without a request to the forge.
// Requirement: 30-REQ-2.6
func TestMissingOrForeignWorkspaceGets404_TS_30_9(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "foreign", func(r *workspaceRow) { r.owner = otherID })

	for _, r := range forgeRoutes {
		for _, slug := range []string{"foreign", "missing"} {
			t.Run(r.name+"/"+slug, func(t *testing.T) {
				rec := e.get(r.url(slug), apiKeyAuth())
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d; want 404\n%s", rec.Code, rec.Body)
				}
				decodeError(t, rec)
			})
		}
	}
	assertNothingLookedUp(t, e)

	// The same routes exist for a workspace the caller owns: a route that is not
	// mounted answers 404 too.
	own := newEnv(t)
	addWorkspace(t, own.db, "widgets")
	for _, r := range forgeRoutes {
		if rec := own.get(r.url("widgets"), apiKeyAuth()); rec.Code == http.StatusNotFound {
			t.Errorf("%s: the owner's own workspace answers 404: %s", r.name, rec.Body)
		}
	}
}

// The scope helper: forge:write implies forge:read, API keys and admin tokens hold it
// implicitly, and a credential type hub does not know holds nothing.
// Requirement: 30-REQ-2.3, 30-REQ-2.4, 30-REQ-2.5
func TestCanReadForge(t *testing.T) {
	cases := []struct {
		name string
		auth *apikit.AuthInfo
		read bool
	}{
		{"api key", apiKeyAuth(), true},
		{"admin token", adminAuth(), true},
		{"pat with forge:read", patAuth("forge:read"), true},
		{"pat with forge:write", patAuth("forge:write"), true},
		{"pat with both", patAuth("forge:read", "forge:write"), true},
		{"pat with another scope", patAuth("sandboxes:write"), false},
		{"pat with no scope", patAuth(), false},
		{"unknown type with the scopes", &apikit.AuthInfo{CredentialType: "mystery", Permissions: []string{"forge:read", "forge:write"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canReadForge(tc.auth); got != tc.read {
				t.Errorf("canReadForge = %v; want %v", got, tc.read)
			}
		})
	}
}

// assertNothingLookedUp fails when a credential was resolved, a variable read or a
// request sent to the forge.
func assertNothingLookedUp(t *testing.T, e *env) {
	t.Helper()
	if n := e.originCalls.Load() + e.upstreamCalls.Load(); n != 0 {
		t.Errorf("%d credential resolutions; want none", n)
	}
	if n := e.varCalls.Load(); n != 0 {
		t.Errorf("%d variable lookups; want none", n)
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
}
