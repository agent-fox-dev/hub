package mergequeue

import "github.com/txsvc/apikit"

// MergeReadScope grants read access to query and list merge jobs.
var MergeReadScope = apikit.Permission{Resource: "merges", Action: "read"}

// MergeWriteScope grants write access to submit, cancel, and requeue merge jobs.
var MergeWriteScope = apikit.Permission{Resource: "merges", Action: "write"}

// Permissions returns all permission scopes registered by the mergequeue
// package. Pass these to apikit.Server.MountHandlers via the extraPerms
// parameter so that PAT creation validates merge scopes.
func Permissions() []apikit.Permission {
	return []apikit.Permission{
		MergeReadScope,
		MergeWriteScope,
	}
}
