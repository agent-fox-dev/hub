package secrets

import "github.com/txsvc/apikit"

// Secrets permission scope constants.
var (
	// SecretsManageScope grants create, list, update, and delete access to secrets.
	// secrets:manage implies secrets:list, secrets:write, and secrets:delete.
	SecretsManageScope = apikit.Permission{Resource: "secrets", Action: "manage"}

	// SecretsListScope grants list access to secret names (values never returned).
	SecretsListScope = apikit.Permission{Resource: "secrets", Action: "list"}

	// SecretsWriteScope grants update access to existing secret values.
	SecretsWriteScope = apikit.Permission{Resource: "secrets", Action: "write"}

	// SecretsDeleteScope grants delete access to secrets.
	SecretsDeleteScope = apikit.Permission{Resource: "secrets", Action: "delete"}
)

// Variables permission scope constants.
var (
	// VarsManageScope grants create, read, update, and delete access to variables.
	// vars:manage implies vars:read, vars:write, and vars:delete.
	VarsManageScope = apikit.Permission{Resource: "vars", Action: "manage"}

	// VarsReadScope grants read access to variables and their values.
	VarsReadScope = apikit.Permission{Resource: "vars", Action: "read"}

	// VarsWriteScope grants update access to existing variables.
	VarsWriteScope = apikit.Permission{Resource: "vars", Action: "write"}

	// VarsDeleteScope grants delete access to variables.
	VarsDeleteScope = apikit.Permission{Resource: "vars", Action: "delete"}
)

// Permissions returns the Permission values that hub registers with
// apikit's MountHandlers for secrets and variables operations.
// The 8 scopes are independent of the workspace scopes.
//
// Implication semantics (e.g. secrets:manage implies secrets:list) are
// enforced at the handler level via OR-checks, not within the registry.
func Permissions() []apikit.Permission {
	return []apikit.Permission{
		SecretsManageScope,
		SecretsListScope,
		SecretsWriteScope,
		SecretsDeleteScope,
		VarsManageScope,
		VarsReadScope,
		VarsWriteScope,
		VarsDeleteScope,
	}
}
