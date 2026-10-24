package secrets

import (
	"testing"

	"github.com/txsvc/apikit"
)

// The secrets and variables scopes are read and write per resource; write
// implies read, read does not imply write.
func TestPermissions_Scopes(t *testing.T) {
	perms := Permissions()
	want := []apikit.Permission{
		{Resource: "secrets", Action: "read"},
		{Resource: "secrets", Action: "write"},
		{Resource: "vars", Action: "read"},
		{Resource: "vars", Action: "write"},
	}
	if len(perms) != len(want) {
		t.Fatalf("Permissions() = %v; want %v", perms, want)
	}
	for i, p := range want {
		if perms[i] != p {
			t.Errorf("Permissions()[%d] = %v; want %v", i, perms[i], p)
		}
	}
}

func pat(scopes ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "pat", UserID: "user-1", Permissions: scopes}
}

func TestPermissions_WriteImpliesRead(t *testing.T) {
	if !canSecretsRead(pat("secrets:write")) {
		t.Error("secrets:write should satisfy canSecretsRead")
	}
	if !canVarsRead(pat("vars:write")) {
		t.Error("vars:write should satisfy canVarsRead")
	}
}

func TestPermissions_ReadDoesNotImplyWrite(t *testing.T) {
	if canSecretsWrite(pat("secrets:read")) {
		t.Error("secrets:read should not satisfy canSecretsWrite")
	}
	if canVarsWrite(pat("vars:read")) {
		t.Error("vars:read should not satisfy canVarsWrite")
	}
}

// The secrets and variables scopes are independent of each other and of the
// workspace scopes.
func TestPermissions_IndependentScopes(t *testing.T) {
	ws := pat("workspaces:read", "workspaces:write")
	if canSecretsRead(ws) || canSecretsWrite(ws) || canVarsRead(ws) || canVarsWrite(ws) {
		t.Error("workspace scopes should grant no secrets or variables access")
	}
	if canVarsRead(pat("secrets:write")) || canSecretsRead(pat("vars:write")) {
		t.Error("secrets and variables scopes should not grant each other")
	}
}
