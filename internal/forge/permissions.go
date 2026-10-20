package forge

import "github.com/txsvc/apikit"

// Forge permission scope constants.
var (
	ForgeReadScope  = apikit.Permission{Resource: "forge", Action: "read"}
	ForgeWriteScope = apikit.Permission{Resource: "forge", Action: "write"}
)

// Permissions returns the two PAT permission scopes for the forge routes. They are
// registered with apikit at every hub start, so that PATs can be issued with them
// ahead of the routes that need forge:write.
func Permissions() []apikit.Permission {
	return []apikit.Permission{
		ForgeReadScope,
		ForgeWriteScope,
	}
}
