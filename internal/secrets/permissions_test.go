package secrets

import (
	"testing"

	"github.com/txsvc/apikit"
)

// TS-07-12: Verifies that secrets:manage, secrets:list, secrets:write, and
// secrets:delete scopes are registered with apikit, and that secrets:manage
// implies the other three.
// Requirement: 07-REQ-6.1
func TestPermissions_SecretsScopes(t *testing.T) {
	perms := Permissions()

	expected := []apikit.Permission{
		{Resource: "secrets", Action: "manage"},
		{Resource: "secrets", Action: "list"},
		{Resource: "secrets", Action: "write"},
		{Resource: "secrets", Action: "delete"},
	}

	for _, exp := range expected {
		found := false
		for _, p := range perms {
			if p.Resource == exp.Resource && p.Action == exp.Action {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected permission %s:%s not found in Permissions(); got %v",
				exp.Resource, exp.Action, perms)
		}
	}
}

// TestPermissions_SecretsManageImpliesList verifies that secrets:manage implies
// secrets:list at the handler level (via canSecretsList OR-check).
// Requirement: 07-REQ-6.1
func TestPermissions_SecretsManageImpliesList(t *testing.T) {
	auth := &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         "user-1",
		Permissions:    []string{"secrets:manage"},
	}
	if !canSecretsList(auth) {
		t.Error("PAT with secrets:manage should satisfy canSecretsList()")
	}
}

// TestPermissions_SecretsManageImpliesWrite verifies that secrets:manage implies
// secrets:write at the handler level (via canSecretsWrite OR-check).
// Requirement: 07-REQ-6.1
func TestPermissions_SecretsManageImpliesWrite(t *testing.T) {
	auth := &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         "user-1",
		Permissions:    []string{"secrets:manage"},
	}
	if !canSecretsWrite(auth) {
		t.Error("PAT with secrets:manage should satisfy canSecretsWrite()")
	}
}

// TestPermissions_SecretsManageImpliesDelete verifies that secrets:manage implies
// secrets:delete at the handler level (via canSecretsDelete OR-check).
// Requirement: 07-REQ-6.1
func TestPermissions_SecretsManageImpliesDelete(t *testing.T) {
	auth := &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         "user-1",
		Permissions:    []string{"secrets:manage"},
	}
	if !canSecretsDelete(auth) {
		t.Error("PAT with secrets:manage should satisfy canSecretsDelete()")
	}
}

// TS-07-13: Verifies that vars:manage, vars:read, vars:write, and vars:delete
// scopes are registered with apikit, with correct implication chains.
// Requirement: 07-REQ-6.2
func TestPermissions_VarsScopes(t *testing.T) {
	perms := Permissions()

	expected := []apikit.Permission{
		{Resource: "vars", Action: "manage"},
		{Resource: "vars", Action: "read"},
		{Resource: "vars", Action: "write"},
		{Resource: "vars", Action: "delete"},
	}

	for _, exp := range expected {
		found := false
		for _, p := range perms {
			if p.Resource == exp.Resource && p.Action == exp.Action {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected permission %s:%s not found in Permissions(); got %v",
				exp.Resource, exp.Action, perms)
		}
	}
}

// TestPermissions_VarsManageImpliesRead verifies that vars:manage implies
// vars:read at the handler level.
// Requirement: 07-REQ-6.2
func TestPermissions_VarsManageImpliesRead(t *testing.T) {
	auth := &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         "user-1",
		Permissions:    []string{"vars:manage"},
	}
	if !canVarsRead(auth) {
		t.Error("PAT with vars:manage should satisfy canVarsRead()")
	}
}

// TestPermissions_VarsManageImpliesWrite verifies that vars:manage implies
// vars:write at the handler level.
// Requirement: 07-REQ-6.2
func TestPermissions_VarsManageImpliesWrite(t *testing.T) {
	auth := &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         "user-1",
		Permissions:    []string{"vars:manage"},
	}
	if !canVarsWrite(auth) {
		t.Error("PAT with vars:manage should satisfy canVarsWrite()")
	}
}

// TestPermissions_VarsManageImpliesDelete verifies that vars:manage implies
// vars:delete at the handler level.
// Requirement: 07-REQ-6.2
func TestPermissions_VarsManageImpliesDelete(t *testing.T) {
	auth := &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         "user-1",
		Permissions:    []string{"vars:manage"},
	}
	if !canVarsDelete(auth) {
		t.Error("PAT with vars:manage should satisfy canVarsDelete()")
	}
}

// TestPermissions_VarsWriteImpliesRead verifies that vars:write implies
// vars:read at the handler level.
// Requirement: 07-REQ-6.2
func TestPermissions_VarsWriteImpliesRead(t *testing.T) {
	auth := &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         "user-1",
		Permissions:    []string{"vars:write"},
	}
	if !canVarsRead(auth) {
		t.Error("PAT with vars:write should satisfy canVarsRead()")
	}
}

// TS-07-14: Verifies that the 8 secrets/vars scopes are registered independently
// of workspace scopes and do not overlap with workspaces:read, workspaces:write,
// workspaces:delete.
// Requirement: 07-REQ-6.3
func TestPermissions_IndependentFromWorkspaceScopes(t *testing.T) {
	perms := Permissions()

	if len(perms) != 8 {
		t.Errorf("expected 8 permissions; got %d: %v", len(perms), perms)
	}

	workspaceResources := map[string]bool{
		"workspaces": true,
	}

	for _, p := range perms {
		if workspaceResources[p.Resource] {
			t.Errorf("permission %s:%s overlaps with workspace scopes", p.Resource, p.Action)
		}
	}

	// Verify all scopes use only the 'secrets' and 'vars' resources.
	allowedResources := map[string]bool{
		"secrets": true,
		"vars":    true,
	}
	for _, p := range perms {
		if !allowedResources[p.Resource] {
			t.Errorf("unexpected resource %q in permission %s:%s; expected 'secrets' or 'vars'",
				p.Resource, p.Resource, p.Action)
		}
	}
}

// TestPermissions_SecretsListDoesNotImplyManage verifies that secrets:list alone
// does NOT satisfy canSecretsManage. This validates the one-directional implication.
// Requirement: 07-REQ-6.E2
func TestPermissions_SecretsListDoesNotImplyManage(t *testing.T) {
	auth := &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         "user-1",
		Permissions:    []string{"secrets:list"},
	}
	if canSecretsManage(auth) {
		t.Error("PAT with only secrets:list should NOT satisfy canSecretsManage()")
	}
}

// TestPermissions_VarsDeleteDoesNotImplyRead verifies that vars:delete alone
// does NOT satisfy canVarsRead. This ensures vars:delete is independent.
// Requirement: 07-REQ-6.2
func TestPermissions_VarsDeleteDoesNotImplyRead(t *testing.T) {
	auth := &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         "user-1",
		Permissions:    []string{"vars:delete"},
	}
	if canVarsRead(auth) {
		t.Error("PAT with only vars:delete should NOT satisfy canVarsRead()")
	}
}
