package secrets

import "github.com/txsvc/apikit"

// The secrets and variables scopes: read and write per resource. secrets:read
// lists secret names (values are never returned); secrets:write creates,
// updates and deletes secrets and implies secrets:read. vars:read lists and
// reads variables; vars:write creates, updates and deletes them and implies
// vars:read.
var (
	SecretsReadScope  = apikit.Permission{Resource: "secrets", Action: "read"}
	SecretsWriteScope = apikit.Permission{Resource: "secrets", Action: "write"}
	VarsReadScope     = apikit.Permission{Resource: "vars", Action: "read"}
	VarsWriteScope    = apikit.Permission{Resource: "vars", Action: "write"}
)

// Permissions returns the Permission values that hub registers with
// apikit's MountHandlers for secrets and variables operations.
func Permissions() []apikit.Permission {
	return []apikit.Permission{SecretsReadScope, SecretsWriteScope, VarsReadScope, VarsWriteScope}
}
