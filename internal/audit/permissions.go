package audit

import "github.com/txsvc/apikit"

// Audit and session permission scope constants.
var (
	AuditReadScope     = apikit.Permission{Resource: "audit", Action: "read"}
	AuditWriteScope    = apikit.Permission{Resource: "audit", Action: "write"}
	SessionsReadScope  = apikit.Permission{Resource: "sessions", Action: "read"}
	SessionsWriteScope = apikit.Permission{Resource: "sessions", Action: "write"}
)

// Permissions returns the four PAT permission scopes for audit and session
// operations. These are registered with the auth system at hub startup.
func Permissions() []apikit.Permission {
	panic("not implemented")
}
