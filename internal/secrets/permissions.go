package secrets

import (
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/vars"
)

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
		vars.VarsManageScope,
		vars.VarsReadScope,
		vars.VarsWriteScope,
		vars.VarsDeleteScope,
	}
}
