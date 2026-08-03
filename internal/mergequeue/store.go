package mergequeue

import (
	"database/sql"
)

// MergeJob represents a single merge request with all fields from the
// merge_jobs table.
type MergeJob struct {
	ID              string
	Nonce           string
	CampaignID      sql.NullString
	SpecID          sql.NullString
	WorkspaceSlug   string
	TargetBranch    string
	SourceRef       string
	Status          string
	RejectionReason sql.NullString
	RetryCount      int
	AvailableAt     string
	BaseSHA         sql.NullString
	MergedSHA       sql.NullString
	ConflictDetails sql.NullString
	CheckOutput     sql.NullString
	SubmittedBy     string
	CreatedAt       string
	UpdatedAt       string
}

// ValidStatuses is the set of allowed merge job status values.
var ValidStatuses = []string{
	"prepared", "queued", "running", "merged",
	"conflict", "check_failed", "cancelled",
	"push_failed", "dead_letter",
}

// InitSchema creates the merge_jobs table and associated indexes.
// It is called during server boot to ensure the schema exists.
// Uses CREATE TABLE IF NOT EXISTS so it is idempotent.
func InitSchema(db *sql.DB) error {
	// TODO: implement schema creation
	return nil
}

// InsertMergeJob inserts a new merge job record with status=prepared.
func InsertMergeJob(db *sql.DB, job *MergeJob) error {
	// TODO: implement insert
	return nil
}

// GetMergeJob retrieves a merge job by ID.
func GetMergeJob(db *sql.DB, id string) (*MergeJob, error) {
	// TODO: implement get
	return nil, nil
}

// UpdateStatus transitions a merge job from its current status to the given
// new status, enforcing the state machine rules. Returns an error if the
// transition is invalid.
func UpdateStatus(db *sql.DB, id string, newStatus string) error {
	// TODO: implement status transition with state machine validation
	return nil
}
