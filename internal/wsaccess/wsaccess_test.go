package wsaccess_test

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// TS-33-17: AuthorizeWorkspace denies a bound PAT for an owned workspace other
// than S and allows S, on a single-connection database.
// Requirements: 33-REQ-3.5, 33-REQ-2.4
func TestAuthorizeWorkspace_Binding_TS_33_17(t *testing.T) {
	db := openBindingDB(t)
	for _, slug := range []string{"S", "X"} {
		if _, err := db.Exec(`INSERT INTO workspaces (slug, owner_id) VALUES (?, 'u1')`, slug); err != nil {
			t.Fatal(err)
		}
	}
	bind(t, db, "tokB", "S")
	bound := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokB", Permissions: []string{"workspaces:read"}}
	unbound := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokU", Permissions: []string{"workspaces:read"}}

	access, code, msg := wsaccess.AuthorizeWorkspace(db, bound, "X")
	if access != nil || code != http.StatusNotFound || msg != "workspace not found" {
		t.Errorf("AuthorizeWorkspace(bound, X) = (%v, %d, %q); want (nil, 404, workspace not found)", access, code, msg)
	}

	got, code, msg := wsaccess.AuthorizeWorkspace(db, bound, "S")
	if got == nil || code != 0 || msg != "" {
		t.Fatalf("AuthorizeWorkspace(bound, S) = (%v, %d, %q); want access", got, code, msg)
	}
	want, _, _ := wsaccess.AuthorizeWorkspace(db, unbound, "S")
	if want == nil || *got != *want {
		t.Errorf("AuthorizeWorkspace(bound, S) = %+v; want the unbound PAT's %+v", got, want)
	}
	// The unbound PAT of the same user still reaches X.
	if a, code, _ := wsaccess.AuthorizeWorkspace(db, unbound, "X"); a == nil || code != 0 {
		t.Errorf("AuthorizeWorkspace(unbound, X) = (%v, %d); want access", a, code)
	}

	// The existence check still answers first for an unknown slug.
	if a, code, msg := wsaccess.AuthorizeWorkspace(db, bound, "ghost"); a != nil || code != http.StatusNotFound || msg != "workspace not found" {
		t.Errorf("AuthorizeWorkspace(bound, ghost) = (%v, %d, %q); want (nil, 404, workspace not found)", a, code, msg)
	}
}
