package secrets

import "github.com/txsvc/apikit"

// Permissions returns the Permission values that hub registers with
// apikit's MountHandlers for secrets and variables operations.
// The 8 scopes are independent of the workspace scopes.
//
// Implication semantics (e.g. secrets:manage implies secrets:list) are
// enforced at the handler level via OR-checks, not within the registry.
func Permissions() []apikit.Permission {
	return []apikit.Permission{
		{Resource: "secrets", Action: "manage"},
		{Resource: "secrets", Action: "list"},
		{Resource: "secrets", Action: "write"},
		{Resource: "secrets", Action: "delete"},
		{Resource: "vars", Action: "manage"},
		{Resource: "vars", Action: "read"},
		{Resource: "vars", Action: "write"},
		{Resource: "vars", Action: "delete"},
	}
}
