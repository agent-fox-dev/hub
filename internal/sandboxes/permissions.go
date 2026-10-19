package sandboxes

import "github.com/txsvc/apikit"

// Sandbox permission scope constants.
var (
	SandboxesReadScope  = apikit.Permission{Resource: "sandboxes", Action: "read"}
	SandboxesWriteScope = apikit.Permission{Resource: "sandboxes", Action: "write"}
)

// Permissions returns the two PAT permission scopes for sandboxes. They are
// registered with apikit at every hub start, whether or not [sandbox] is
// configured, so that PATs can be issued with them ahead of time.
func Permissions() []apikit.Permission {
	return []apikit.Permission{
		SandboxesReadScope,
		SandboxesWriteScope,
	}
}
