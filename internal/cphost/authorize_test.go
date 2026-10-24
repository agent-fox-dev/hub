package cphost

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/carrypatch"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// The module's access rule is the hub's: no credential is 401, owners and
// admin tokens read and write, org members read but may not write, and a
// workspace the caller may not see (or that does not exist) is 404.
func TestAuthorize_AppliesHubWorkspaceAccessRule(t *testing.T) {
	h := newHub(t)
	tests := []struct {
		name string
		auth *apikit.AuthInfo
		slug string
		need carrypatch.Need
		want int
	}{
		{"no credential", nil, "myws", carrypatch.NeedRead, http.StatusUnauthorized},
		{"owner writes", testutil.User(h.owner), "myws", carrypatch.NeedWrite, 0},
		{"admin writes", testutil.Admin(), "myws", carrypatch.NeedWrite, 0},
		{"member reads", testutil.User(h.member), "myws", carrypatch.NeedRead, 0},
		{"member may not write", testutil.User(h.member), "myws", carrypatch.NeedWrite, http.StatusForbidden},
		{"stranger", testutil.User("nobody"), "myws", carrypatch.NeedRead, http.StatusNotFound},
		{"unknown workspace", testutil.User(h.owner), "nope", carrypatch.NeedRead, http.StatusNotFound},
	}
	for _, tt := range tests {
		if code, _ := authorize(h.db, tt.auth, tt.slug, tt.need); code != tt.want {
			t.Errorf("%s: authorize = %d, want %d", tt.name, code, tt.want)
		}
	}
}

// Bind hands that rule to the module, so its routes answer with it.
func TestBind_InstallsAccessRuleInModule(t *testing.T) {
	h := newHub(t)
	Bind()
	e, api := testutil.API()
	carrypatch.RegisterPatchRoutes(api, carrypatch.APIConfig{DB: h.db, PatchStore: carrypatch.NewSQLPatchStore(h.db)})
	for user, want := range map[string]int{h.owner: http.StatusOK, "nobody": http.StatusNotFound} {
		if rec := testutil.Request(t, e, http.MethodGet, "/api/v1/workspaces/myws/patches", "", testutil.User(user)); rec.Code != want {
			t.Errorf("GET patches as %s = %d, want %d: %s", user, rec.Code, want, rec.Body)
		}
	}
}
