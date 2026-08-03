package campaign

import (
	"database/sql"
	"fmt"
)

const campaignsTableSQL = `
CREATE TABLE IF NOT EXISTS campaigns (
	id                TEXT PRIMARY KEY,
	workspace_slug    TEXT NOT NULL,
	name              TEXT NOT NULL,
	integration_branch TEXT NOT NULL,
	status            TEXT NOT NULL CHECK(status IN ('pending','active','completed','failed','cancelled')),
	dag               TEXT NOT NULL,
	created_by        TEXT NOT NULL,
	created_at        TEXT NOT NULL,
	updated_at        TEXT NOT NULL,
	UNIQUE(workspace_slug, name)
)`

const campaignSpecsTableSQL = `
CREATE TABLE IF NOT EXISTS campaign_specs (
	campaign_id       TEXT NOT NULL REFERENCES campaigns(id),
	spec_id           TEXT NOT NULL,
	status            TEXT NOT NULL CHECK(status IN ('pending','active','merged','blocked','failed','cancelled')),
	branch_name       TEXT NOT NULL DEFAULT '',
	branch_sha        TEXT NOT NULL DEFAULT '',
	conflict_details  TEXT,
	blocked_by_merge  TEXT,
	updated_at        TEXT NOT NULL,
	PRIMARY KEY (campaign_id, spec_id)
)`

// InitSchema creates the campaigns and campaign_specs tables with all columns,
// constraints, and CHECK constraints. It is called during hub startup.
func InitSchema(db *sql.DB) error {
	if _, err := db.Exec(campaignsTableSQL); err != nil {
		return fmt.Errorf("create campaigns table: %w", err)
	}
	if _, err := db.Exec(campaignSpecsTableSQL); err != nil {
		return fmt.Errorf("create campaign_specs table: %w", err)
	}
	return nil
}
