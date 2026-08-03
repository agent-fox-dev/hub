package campaign

import "github.com/txsvc/apikit"

// CampaignReadScope grants read access to campaign status and DAG.
var CampaignReadScope = apikit.Permission{Resource: "campaigns", Action: "read"}

// CampaignWriteScope grants write access to create, cancel campaigns,
// and resolve conflicts.
var CampaignWriteScope = apikit.Permission{Resource: "campaigns", Action: "write"}

// Permissions returns all permission scopes registered by the campaign package.
// Pass these to apikit.Server.MountHandlers via the extraPerms parameter so
// that PAT creation validates campaign scopes.
func Permissions() []apikit.Permission {
	return []apikit.Permission{
		CampaignReadScope,
		CampaignWriteScope,
	}
}
