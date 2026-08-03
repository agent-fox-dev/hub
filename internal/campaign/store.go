package campaign

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Valid campaign status transitions. A transition is allowed if the
// destination status appears in the list for the source status.
var validCampaignTransitions = map[string][]string{
	"pending":   {"active", "cancelled"},
	"active":    {"completed", "failed", "cancelled"},
	"completed": {},
	"failed":    {},
	"cancelled": {},
}

// Store provides database operations for campaigns and campaign_specs.
type Store struct {
	db *sql.DB
}

// NewStore creates a new campaign Store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// CreateCampaign inserts a new campaign row. The campaign's ID, CreatedAt,
// and UpdatedAt must be set by the caller before calling this method.
func (s *Store) CreateCampaign(_ context.Context, c *Campaign) error {
	dagJSON, err := SerializeDAG(c.DAG)
	if err != nil {
		return fmt.Errorf("create campaign: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO campaigns (id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.WorkspaceSlug, c.Name, c.IntegrationBranch, c.Status, dagJSON, c.CreatedBy, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create campaign %q: %w", c.Name, err)
	}
	return nil
}

// GetCampaign retrieves a campaign by ID.
func (s *Store) GetCampaign(_ context.Context, id string) (*Campaign, error) {
	c := &Campaign{}
	var dagJSON string
	err := s.db.QueryRow(
		`SELECT id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at
		 FROM campaigns WHERE id = ?`, id,
	).Scan(&c.ID, &c.WorkspaceSlug, &c.Name, &c.IntegrationBranch, &c.Status, &dagJSON, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get campaign %q: %w", id, err)
	}
	dag, err := DeserializeDAG(dagJSON)
	if err != nil {
		return nil, fmt.Errorf("get campaign %q: %w", id, err)
	}
	c.DAG = dag
	return c, nil
}

// GetCampaignByName retrieves a campaign by workspace slug and name.
func (s *Store) GetCampaignByName(_ context.Context, slug, name string) (*Campaign, error) {
	c := &Campaign{}
	var dagJSON string
	err := s.db.QueryRow(
		`SELECT id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at
		 FROM campaigns WHERE workspace_slug = ? AND name = ?`, slug, name,
	).Scan(&c.ID, &c.WorkspaceSlug, &c.Name, &c.IntegrationBranch, &c.Status, &dagJSON, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get campaign by name %q: %w", name, err)
	}
	dag, err := DeserializeDAG(dagJSON)
	if err != nil {
		return nil, fmt.Errorf("get campaign by name %q: %w", name, err)
	}
	c.DAG = dag
	return c, nil
}

// UpdateCampaignStatus updates the status of a campaign. It validates
// the transition is allowed and returns an error for invalid transitions.
func (s *Store) UpdateCampaignStatus(ctx context.Context, id, newStatus string) error {
	// Read current status to validate transition.
	current, err := s.GetCampaign(ctx, id)
	if err != nil {
		return fmt.Errorf("update campaign status: %w", err)
	}
	if current == nil {
		return fmt.Errorf("campaign %q not found", id)
	}

	allowed, ok := validCampaignTransitions[current.Status]
	if !ok {
		return fmt.Errorf("invalid current status %q for campaign %q", current.Status, id)
	}
	valid := false
	for _, s := range allowed {
		if s == newStatus {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid status transition %q → %q for campaign %q", current.Status, newStatus, id)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.Exec(
		`UPDATE campaigns SET status = ?, updated_at = ? WHERE id = ?`,
		newStatus, now, id,
	)
	if err != nil {
		return fmt.Errorf("update campaign status %q: %w", id, err)
	}
	return nil
}

// CreateCampaignSpec inserts a new campaign_specs row.
func (s *Store) CreateCampaignSpec(_ context.Context, cs *CampaignSpec) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if cs.UpdatedAt == "" {
		cs.UpdatedAt = now
	}

	var conflictJSON *string
	if len(cs.ConflictDetails) > 0 {
		serialized, err := SerializeConflictDetails(cs.ConflictDetails)
		if err != nil {
			return fmt.Errorf("create campaign spec: %w", err)
		}
		conflictJSON = &serialized
	}

	var blockedBy *string
	if cs.BlockedByMerge != "" {
		blockedBy = &cs.BlockedByMerge
	}

	_, err := s.db.Exec(
		`INSERT INTO campaign_specs (campaign_id, spec_id, status, branch_name, branch_sha, conflict_details, blocked_by_merge, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		cs.CampaignID, cs.SpecID, cs.Status, cs.BranchName, cs.BranchSHA, conflictJSON, blockedBy, cs.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create campaign spec %q/%q: %w", cs.CampaignID, cs.SpecID, err)
	}
	return nil
}

// GetCampaignSpecs retrieves all campaign_specs rows for a campaign.
func (s *Store) GetCampaignSpecs(_ context.Context, campaignID string) ([]CampaignSpec, error) {
	rows, err := s.db.Query(
		`SELECT campaign_id, spec_id, status, branch_name, branch_sha, conflict_details, blocked_by_merge, updated_at
		 FROM campaign_specs WHERE campaign_id = ? ORDER BY spec_id`, campaignID,
	)
	if err != nil {
		return nil, fmt.Errorf("get campaign specs for %q: %w", campaignID, err)
	}
	defer rows.Close()

	return scanCampaignSpecs(rows)
}

// scanCampaignSpecs scans campaign_specs rows into a slice.
func scanCampaignSpecs(rows *sql.Rows) ([]CampaignSpec, error) {
	var specs []CampaignSpec
	for rows.Next() {
		var cs CampaignSpec
		var conflictJSON sql.NullString
		var blockedBy sql.NullString
		if err := rows.Scan(
			&cs.CampaignID, &cs.SpecID, &cs.Status, &cs.BranchName, &cs.BranchSHA,
			&conflictJSON, &blockedBy, &cs.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan campaign spec: %w", err)
		}
		if conflictJSON.Valid && conflictJSON.String != "" {
			details, err := DeserializeConflictDetails(conflictJSON.String)
			if err != nil {
				return nil, fmt.Errorf("deserialize conflict details: %w", err)
			}
			cs.ConflictDetails = details
		}
		if blockedBy.Valid {
			cs.BlockedByMerge = blockedBy.String
		}
		specs = append(specs, cs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate campaign specs: %w", err)
	}
	if specs == nil {
		specs = []CampaignSpec{}
	}
	return specs, nil
}

// UpdateSpecStatus updates the status of a campaign spec.
func (s *Store) UpdateSpecStatus(_ context.Context, campaignID, specID, newStatus string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.Exec(
		`UPDATE campaign_specs SET status = ?, updated_at = ? WHERE campaign_id = ? AND spec_id = ?`,
		newStatus, now, campaignID, specID,
	)
	if err != nil {
		return fmt.Errorf("update spec status %q/%q: %w", campaignID, specID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update spec status %q/%q: rows affected: %w", campaignID, specID, err)
	}
	if affected == 0 {
		return fmt.Errorf("campaign spec %q/%q not found", campaignID, specID)
	}
	return nil
}

// HasActiveCampaignForBranch checks if an active campaign exists for the given
// workspace and integration branch combination.
func (s *Store) HasActiveCampaignForBranch(_ context.Context, slug, branch string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM campaigns WHERE workspace_slug = ? AND integration_branch = ? AND status = 'active'`,
		slug, branch,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check active campaign for branch: %w", err)
	}
	return count > 0, nil
}

// CancelCampaign sets the campaign and all its specs to cancelled status
// in a single transaction.
func (s *Store) CancelCampaign(_ context.Context, id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("cancel campaign: begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)

	_, err = tx.Exec(
		`UPDATE campaigns SET status = 'cancelled', updated_at = ? WHERE id = ?`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("cancel campaign %q: %w", id, err)
	}

	_, err = tx.Exec(
		`UPDATE campaign_specs SET status = 'cancelled', updated_at = ? WHERE campaign_id = ?`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("cancel campaign specs %q: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cancel campaign: commit: %w", err)
	}
	return nil
}

// ListCampaigns returns campaigns for a workspace, optionally filtered by status.
// If status is empty, all campaigns are returned.
func (s *Store) ListCampaigns(_ context.Context, slug, status string) ([]Campaign, error) {
	query := `SELECT id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at
		 FROM campaigns WHERE workspace_slug = ?`
	args := []any{slug}

	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list campaigns for %q: %w", slug, err)
	}
	defer rows.Close()

	var campaigns []Campaign
	for rows.Next() {
		var c Campaign
		var dagJSON string
		if err := rows.Scan(&c.ID, &c.WorkspaceSlug, &c.Name, &c.IntegrationBranch, &c.Status, &dagJSON, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan campaign: %w", err)
		}
		dag, err := DeserializeDAG(dagJSON)
		if err != nil {
			return nil, fmt.Errorf("list campaigns: %w", err)
		}
		c.DAG = dag
		campaigns = append(campaigns, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate campaigns: %w", err)
	}
	if campaigns == nil {
		campaigns = []Campaign{}
	}
	return campaigns, nil
}

// GetCampaignSpec retrieves a single campaign_specs row by campaign ID and spec ID.
func (s *Store) GetCampaignSpec(_ context.Context, campaignID, specID string) (*CampaignSpec, error) {
	var cs CampaignSpec
	var conflictJSON sql.NullString
	var blockedBy sql.NullString
	err := s.db.QueryRow(
		`SELECT campaign_id, spec_id, status, branch_name, branch_sha, conflict_details, blocked_by_merge, updated_at
		 FROM campaign_specs WHERE campaign_id = ? AND spec_id = ?`, campaignID, specID,
	).Scan(&cs.CampaignID, &cs.SpecID, &cs.Status, &cs.BranchName, &cs.BranchSHA, &conflictJSON, &blockedBy, &cs.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get campaign spec %q/%q: %w", campaignID, specID, err)
	}
	if conflictJSON.Valid && conflictJSON.String != "" {
		details, err := DeserializeConflictDetails(conflictJSON.String)
		if err != nil {
			return nil, fmt.Errorf("deserialize conflict details: %w", err)
		}
		cs.ConflictDetails = details
	}
	if blockedBy.Valid {
		cs.BlockedByMerge = blockedBy.String
	}
	return &cs, nil
}

// UpdateSpecBranchSHA updates the branch_sha for a campaign spec after a
// clean rebase.
func (s *Store) UpdateSpecBranchSHA(_ context.Context, campaignID, specID, sha string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.Exec(
		`UPDATE campaign_specs SET branch_sha = ?, updated_at = ? WHERE campaign_id = ? AND spec_id = ?`,
		sha, now, campaignID, specID,
	)
	if err != nil {
		return fmt.Errorf("update spec branch SHA %q/%q: %w", campaignID, specID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update spec branch SHA %q/%q: rows affected: %w", campaignID, specID, err)
	}
	if affected == 0 {
		return fmt.Errorf("campaign spec %q/%q not found", campaignID, specID)
	}
	return nil
}

// SetSpecBlocked sets a spec to blocked status with conflict details and the
// ID of the merge job that triggered the conflict.
func (s *Store) SetSpecBlocked(_ context.Context, campaignID, specID string, conflictFiles []string, blockedByMerge string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	conflictJSON, err := SerializeConflictDetails(conflictFiles)
	if err != nil {
		return fmt.Errorf("set spec blocked: %w", err)
	}
	result, err := s.db.Exec(
		`UPDATE campaign_specs SET status = 'blocked', conflict_details = ?, blocked_by_merge = ?, updated_at = ?
		 WHERE campaign_id = ? AND spec_id = ?`,
		conflictJSON, blockedByMerge, now, campaignID, specID,
	)
	if err != nil {
		return fmt.Errorf("set spec blocked %q/%q: %w", campaignID, specID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set spec blocked %q/%q: rows affected: %w", campaignID, specID, err)
	}
	if affected == 0 {
		return fmt.Errorf("campaign spec %q/%q not found", campaignID, specID)
	}
	return nil
}

// ActivateSpec transitions a pending spec to active with its branch info.
// Used when advancing the DAG frontier after a successful merge.
func (s *Store) ActivateSpec(_ context.Context, campaignID, specID, branchName, branchSHA string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.Exec(
		`UPDATE campaign_specs SET status = 'active', branch_name = ?, branch_sha = ?, updated_at = ?
		 WHERE campaign_id = ? AND spec_id = ?`,
		branchName, branchSHA, now, campaignID, specID,
	)
	if err != nil {
		return fmt.Errorf("activate spec %q/%q: %w", campaignID, specID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("activate spec %q/%q: rows affected: %w", campaignID, specID, err)
	}
	if affected == 0 {
		return fmt.Errorf("campaign spec %q/%q not found", campaignID, specID)
	}
	return nil
}

// ResolveSpec transitions a blocked spec back to active after a clean rebase.
// Clears conflict_details and blocked_by_merge, and updates the branch_sha.
func (s *Store) ResolveSpec(_ context.Context, campaignID, specID, branchSHA string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.Exec(
		`UPDATE campaign_specs SET status = 'active', branch_sha = ?, conflict_details = NULL, blocked_by_merge = NULL, updated_at = ?
		 WHERE campaign_id = ? AND spec_id = ?`,
		branchSHA, now, campaignID, specID,
	)
	if err != nil {
		return fmt.Errorf("resolve spec %q/%q: %w", campaignID, specID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("resolve spec %q/%q: rows affected: %w", campaignID, specID, err)
	}
	if affected == 0 {
		return fmt.Errorf("campaign spec %q/%q not found", campaignID, specID)
	}
	return nil
}

// ListActiveCampaigns returns all campaigns with status=active.
// Used during crash recovery to recompute frontiers.
func (s *Store) ListActiveCampaigns(_ context.Context) ([]Campaign, error) {
	rows, err := s.db.Query(
		`SELECT id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at
		 FROM campaigns WHERE status = 'active' ORDER BY created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("list active campaigns: %w", err)
	}
	defer rows.Close()

	var campaigns []Campaign
	for rows.Next() {
		var c Campaign
		var dagJSON string
		if err := rows.Scan(&c.ID, &c.WorkspaceSlug, &c.Name, &c.IntegrationBranch, &c.Status, &dagJSON, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan active campaign: %w", err)
		}
		dag, err := DeserializeDAG(dagJSON)
		if err != nil {
			return nil, fmt.Errorf("list active campaigns: %w", err)
		}
		c.DAG = dag
		campaigns = append(campaigns, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active campaigns: %w", err)
	}
	if campaigns == nil {
		campaigns = []Campaign{}
	}
	return campaigns, nil
}
