package campaign

import (
	"context"
	"database/sql"
)

// Store provides database operations for campaigns and campaign_specs.
type Store struct {
	db *sql.DB
}

// NewStore creates a new campaign Store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// CreateCampaign inserts a new campaign row.
func (s *Store) CreateCampaign(_ context.Context, _ *Campaign) error {
	return nil // stub
}

// GetCampaign retrieves a campaign by ID.
func (s *Store) GetCampaign(_ context.Context, _ string) (*Campaign, error) {
	return nil, nil // stub
}

// GetCampaignByName retrieves a campaign by workspace slug and name.
func (s *Store) GetCampaignByName(_ context.Context, _, _ string) (*Campaign, error) {
	return nil, nil // stub
}

// UpdateCampaignStatus updates the status of a campaign.
func (s *Store) UpdateCampaignStatus(_ context.Context, _, _ string) error {
	return nil // stub
}

// CreateCampaignSpec inserts a new campaign_specs row.
func (s *Store) CreateCampaignSpec(_ context.Context, _ *CampaignSpec) error {
	return nil // stub
}

// GetCampaignSpecs retrieves all campaign_specs rows for a campaign.
func (s *Store) GetCampaignSpecs(_ context.Context, _ string) ([]CampaignSpec, error) {
	return nil, nil // stub
}

// UpdateSpecStatus updates the status of a campaign spec.
func (s *Store) UpdateSpecStatus(_ context.Context, _, _, _ string) error {
	return nil // stub
}

// HasActiveCampaignForBranch checks if an active campaign exists for the given
// workspace and integration branch combination.
func (s *Store) HasActiveCampaignForBranch(_ context.Context, _, _ string) (bool, error) {
	return false, nil // stub
}

// CancelCampaign sets the campaign and all its specs to cancelled status.
func (s *Store) CancelCampaign(_ context.Context, _ string) error {
	return nil // stub
}
