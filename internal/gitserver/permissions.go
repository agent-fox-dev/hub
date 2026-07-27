package gitserver

import "github.com/txsvc/apikit"

// Git permission scope constants.
var (
	// GitReadScope grants clone and fetch access to workspace repositories.
	GitReadScope = apikit.Permission{Resource: "git", Action: "read"}

	// GitWriteScope grants push access to workspace repositories.
	// git:write implies git:read — a PAT with only git:write may perform
	// both fetch and push operations.
	GitWriteScope = apikit.Permission{Resource: "git", Action: "write"}
)

// GitPermissions returns the Permission values that the git server registers
// with apikit's permission registry for PAT issuance and validation.
//
// The caller (typically main.go) passes these to apikit.Server.MountHandlers
// alongside other module permissions so that apikit recognises them during
// PAT creation and validation.
func GitPermissions() []apikit.Permission {
	return []apikit.Permission{
		GitReadScope,
		GitWriteScope,
	}
}
