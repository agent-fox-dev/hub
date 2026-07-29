package vars

import "github.com/txsvc/apikit"

// Variable permission scope constants.
var (
	// VarsManageScope grants create, list, read, update, and delete access to variables.
	// vars:manage implies vars:read, vars:write, and vars:delete.
	VarsManageScope = apikit.Permission{Resource: "vars", Action: "manage"}

	// VarsReadScope grants list and read access to variable values.
	VarsReadScope = apikit.Permission{Resource: "vars", Action: "read"}

	// VarsWriteScope grants list, read, and update access to variable values.
	// vars:write implies vars:read.
	VarsWriteScope = apikit.Permission{Resource: "vars", Action: "write"}

	// VarsDeleteScope grants delete access to variables.
	VarsDeleteScope = apikit.Permission{Resource: "vars", Action: "delete"}
)

// VarsPermissions returns the Permission values for variables operations.
// These scopes are also included in secrets.Permissions() which combines
// all 8 secrets and variables scopes for hub startup registration.
//
// Implication semantics (vars:manage implies vars:read, vars:write,
// vars:delete; vars:write implies vars:read) are enforced at the handler
// level via OR-checks, not within the apikit permission registry.
func VarsPermissions() []apikit.Permission {
	return []apikit.Permission{
		VarsManageScope,
		VarsReadScope,
		VarsWriteScope,
		VarsDeleteScope,
	}
}
