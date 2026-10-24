package sandboxes

import (
	"slices"
	"testing"

	"github.com/txsvc/apikit"
)

// TS-25-36: Permissions returns exactly sandboxes:read and sandboxes:write, in
// that order.
// Requirement: 25-REQ-6.1
func TestPermissions_TS_25_36(t *testing.T) {
	want := []apikit.Permission{
		{Resource: "sandboxes", Action: "read"},
		{Resource: "sandboxes", Action: "write"},
	}
	if got := Permissions(); !slices.Equal(got, want) {
		t.Errorf("Permissions() = %v; want %v", got, want)
	}
}

// scopePool is what the access property tests draw permission lists from: the
// two sandbox scopes, scopes of other resources and near misses.
var scopePool = []string{
	"sandboxes:read",
	"sandboxes:write",
	"secrets:read",
	"workspaces:read",
	"sandbox:read",
	"sandboxes:readonly",
	"Sandboxes:Read",
}

// eachPermissionList calls fn with every subset of scopePool, in pool order
// and in reverse order, so that neither the content nor the position of a
// scope matters. The empty list is the first subset.
func eachPermissionList(fn func(perms []string)) {
	for mask := range 1 << len(scopePool) {
		var perms []string
		for i, scope := range scopePool {
			if mask&(1<<i) != 0 {
				perms = append(perms, scope)
			}
		}
		fn(perms)
		reversed := slices.Clone(perms)
		slices.Reverse(reversed)
		fn(reversed)
	}
}

// TS-25-37: For any PAT, read access holds exactly when its permissions
// contain sandboxes:read or sandboxes:write.
// Requirement: 25-REQ-6.2
func TestCanReadSandboxes_PAT_TS_25_37(t *testing.T) {
	checked := 0
	eachPermissionList(func(perms []string) {
		auth := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", Permissions: perms}
		want := slices.Contains(perms, "sandboxes:read") || slices.Contains(perms, "sandboxes:write")
		if got := canReadSandboxes(auth); got != want {
			t.Errorf("canReadSandboxes(pat %v) = %v; want %v", perms, got, want)
		}
		checked++
	})
	if checked != 2<<len(scopePool) {
		t.Fatalf("checked %d permission lists; want %d", checked, 2<<len(scopePool))
	}
}

// TS-25-38: Any api_key or admin_token credential gets read access whatever
// its permissions, and an unknown credential type gets none.
// Requirement: 25-REQ-6.3
func TestCanReadSandboxes_APIKeyAndAdmin_TS_25_38(t *testing.T) {
	for _, credential := range []string{"api_key", "admin_token"} {
		eachPermissionList(func(perms []string) {
			auth := &apikit.AuthInfo{CredentialType: credential, Permissions: perms}
			if !canReadSandboxes(auth) {
				t.Errorf("canReadSandboxes(%s with %v) = false; want true", credential, perms)
			}
		})
	}

	// Not part of the requirement: a credential type hub does not know must
	// not read by default.
	unknown := &apikit.AuthInfo{CredentialType: "service", Permissions: []string{"sandboxes:read"}}
	if canReadSandboxes(unknown) {
		t.Error("canReadSandboxes(unknown credential type) = true; want false")
	}
}
