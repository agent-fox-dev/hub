package audit

import (
	"testing"
)

// TS-17-8: audit.Permissions() returns exactly four apikit.Permission entries
// with the correct scope names: audit:read, audit:write, sessions:read,
// sessions:write.
func TestPermissions_FourScopesReturned(t *testing.T) {
	perms := Permissions()

	if len(perms) != 4 {
		t.Fatalf("Permissions() returned %d entries, want 4", len(perms))
	}

	// Build a set of "resource:action" scope strings
	scopes := make(map[string]bool, len(perms))
	for _, p := range perms {
		scopes[p.Resource+":"+p.Action] = true
	}

	expected := []string{"audit:read", "audit:write", "sessions:read", "sessions:write"}
	for _, s := range expected {
		if !scopes[s] {
			t.Errorf("missing expected scope %q in Permissions()", s)
		}
	}
}

// TS-17-8 (supplementary): No duplicate scopes in Permissions().
func TestPermissions_NoDuplicates(t *testing.T) {
	perms := Permissions()

	seen := make(map[string]bool, len(perms))
	for _, p := range perms {
		key := p.Resource + ":" + p.Action
		if seen[key] {
			t.Errorf("duplicate scope %q in Permissions()", key)
		}
		seen[key] = true
	}
}
