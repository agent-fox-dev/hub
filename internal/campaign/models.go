package campaign

// Campaign represents a row in the campaigns table.
type Campaign struct {
	ID                string `json:"id"`
	WorkspaceSlug     string `json:"workspace_slug"`
	Name              string `json:"name"`
	IntegrationBranch string `json:"integration_branch"`
	Status            string `json:"status"`
	DAG               *DAG   `json:"dag,omitempty"`
	CreatedBy         string `json:"created_by"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

// CampaignSpec represents a row in the campaign_specs table.
type CampaignSpec struct {
	CampaignID      string   `json:"campaign_id"`
	SpecID          string   `json:"spec_id"`
	Status          string   `json:"status"`
	BranchName      string   `json:"branch_name,omitempty"`
	BranchSHA       string   `json:"branch_sha,omitempty"`
	ConflictDetails []string `json:"conflict_details,omitempty"`
	BlockedByMerge  string   `json:"blocked_by_merge,omitempty"`
	UpdatedAt       string   `json:"updated_at"`
}

// DAG represents the spec dependency graph stored in the campaigns.dag column.
type DAG struct {
	Specs []string `json:"specs"`
	Edges []Edge   `json:"edges"`
}

// Edge represents a directed dependency edge between two specs.
type Edge struct {
	From         string `json:"from"`
	To           string `json:"to"`
	Relationship string `json:"relationship"`
}
