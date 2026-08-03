package mergequeue

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/txsvc/apikit"
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

// ErrInvalidTransition is returned when a state machine transition is not
// permitted by the merge job lifecycle rules.
var ErrInvalidTransition = errors.New("invalid status transition")

// allowedTransitions defines the valid state machine edges.
// Key: current status, Value: set of permitted target statuses.
var allowedTransitions = map[string]map[string]bool{
	"prepared":     {"queued": true},
	"queued":       {"running": true, "cancelled": true, "dead_letter": true},
	"running":      {"merged": true, "conflict": true, "check_failed": true, "push_failed": true},
	"merged":       {},
	"conflict":     {},
	"check_failed": {},
	"push_failed":  {},
	"cancelled":    {},
	"dead_letter":  {},
}

// ListOptions configures the ListMergeJobs query.
type ListOptions struct {
	Status    string // optional status filter
	AfterID   string // cursor: job ID to start after
	AfterTime string // cursor: created_at of the after job
	Limit     int    // max items to return (default 50, max 100)
}

// InitSchema creates the merge_jobs table and associated indexes.
// It is called during server boot to ensure the schema exists.
// Uses CREATE TABLE IF NOT EXISTS so it is idempotent.
func InitSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS merge_jobs (
			id               TEXT PRIMARY KEY,
			nonce            TEXT NOT NULL UNIQUE,
			campaign_id      TEXT,
			spec_id          TEXT,
			workspace_slug   TEXT NOT NULL,
			target_branch    TEXT NOT NULL,
			source_ref       TEXT NOT NULL,
			status           TEXT NOT NULL
				CHECK(status IN ('prepared','queued','running','merged',
					'conflict','check_failed','cancelled','push_failed','dead_letter')),
			rejection_reason TEXT,
			retry_count      INTEGER NOT NULL,
			available_at     TEXT NOT NULL,
			base_sha         TEXT,
			merged_sha       TEXT,
			conflict_details TEXT,
			check_output     TEXT,
			submitted_by     TEXT NOT NULL,
			created_at       TEXT NOT NULL,
			updated_at       TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_merge_jobs_campaign
			ON merge_jobs(campaign_id, status);
		CREATE INDEX IF NOT EXISTS idx_merge_jobs_workspace
			ON merge_jobs(workspace_slug, status, created_at);
		CREATE INDEX IF NOT EXISTS idx_merge_jobs_available
			ON merge_jobs(status, available_at);
	`)
	return err
}

// InsertMergeJob inserts a new merge job record.
func InsertMergeJob(db *sql.DB, job *MergeJob) error {
	_, err := db.Exec(`INSERT INTO merge_jobs (
		id, nonce, campaign_id, spec_id, workspace_slug, target_branch, source_ref,
		status, rejection_reason, retry_count, available_at, base_sha, merged_sha,
		conflict_details, check_output, submitted_by, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Nonce, job.CampaignID, job.SpecID,
		job.WorkspaceSlug, job.TargetBranch, job.SourceRef,
		job.Status, job.RejectionReason, job.RetryCount,
		job.AvailableAt, job.BaseSHA, job.MergedSHA,
		job.ConflictDetails, job.CheckOutput, job.SubmittedBy,
		job.CreatedAt, job.UpdatedAt,
	)
	return err
}

// GetMergeJob retrieves a merge job by ID. Returns (nil, nil) if not found.
func GetMergeJob(db *sql.DB, id string) (*MergeJob, error) {
	var job MergeJob
	err := db.QueryRow(`SELECT
		id, nonce, campaign_id, spec_id, workspace_slug, target_branch, source_ref,
		status, rejection_reason, retry_count, available_at, base_sha, merged_sha,
		conflict_details, check_output, submitted_by, created_at, updated_at
	FROM merge_jobs WHERE id = ?`, id).Scan(
		&job.ID, &job.Nonce, &job.CampaignID, &job.SpecID,
		&job.WorkspaceSlug, &job.TargetBranch, &job.SourceRef,
		&job.Status, &job.RejectionReason, &job.RetryCount,
		&job.AvailableAt, &job.BaseSHA, &job.MergedSHA,
		&job.ConflictDetails, &job.CheckOutput, &job.SubmittedBy,
		&job.CreatedAt, &job.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// UpdateStatus transitions a merge job from its current status to the given
// new status, enforcing the state machine rules. Returns ErrInvalidTransition
// if the transition is not allowed. Always updates updated_at to now().
func UpdateStatus(db *sql.DB, id string, newStatus string) error {
	var currentStatus string
	err := db.QueryRow("SELECT status FROM merge_jobs WHERE id = ?", id).Scan(&currentStatus)
	if err != nil {
		return fmt.Errorf("query current status: %w", err)
	}

	targets, ok := allowedTransitions[currentStatus]
	if !ok || !targets[newStatus] {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, currentStatus, newStatus)
	}

	now := apikit.NowUTC()
	_, err = db.Exec(
		"UPDATE merge_jobs SET status = ?, updated_at = ? WHERE id = ?",
		newStatus, now, id,
	)
	return err
}

// FindActiveJob queries for an active merge job (status IN ('queued','running'))
// for the given (workspace_slug, source_ref) pair. Returns nil if none exists.
func FindActiveJob(db *sql.DB, workspaceSlug, sourceRef string) (*MergeJob, error) {
	var job MergeJob
	err := db.QueryRow(`SELECT
		id, nonce, campaign_id, spec_id, workspace_slug, target_branch, source_ref,
		status, rejection_reason, retry_count, available_at, base_sha, merged_sha,
		conflict_details, check_output, submitted_by, created_at, updated_at
	FROM merge_jobs
	WHERE workspace_slug = ? AND source_ref = ? AND status IN ('queued', 'running')
	LIMIT 1`, workspaceSlug, sourceRef).Scan(
		&job.ID, &job.Nonce, &job.CampaignID, &job.SpecID,
		&job.WorkspaceSlug, &job.TargetBranch, &job.SourceRef,
		&job.Status, &job.RejectionReason, &job.RetryCount,
		&job.AvailableAt, &job.BaseSHA, &job.MergedSHA,
		&job.ConflictDetails, &job.CheckOutput, &job.SubmittedBy,
		&job.CreatedAt, &job.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// NextAvailableJob returns the next queued job with available_at <= now().
// Returns nil if no eligible job exists.
func NextAvailableJob(db *sql.DB) (*MergeJob, error) {
	now := apikit.NowUTC()
	var job MergeJob
	err := db.QueryRow(`SELECT
		id, nonce, campaign_id, spec_id, workspace_slug, target_branch, source_ref,
		status, rejection_reason, retry_count, available_at, base_sha, merged_sha,
		conflict_details, check_output, submitted_by, created_at, updated_at
	FROM merge_jobs
	WHERE status = 'queued' AND available_at <= ?
	ORDER BY available_at ASC
	LIMIT 1`, now).Scan(
		&job.ID, &job.Nonce, &job.CampaignID, &job.SpecID,
		&job.WorkspaceSlug, &job.TargetBranch, &job.SourceRef,
		&job.Status, &job.RejectionReason, &job.RetryCount,
		&job.AvailableAt, &job.BaseSHA, &job.MergedSHA,
		&job.ConflictDetails, &job.CheckOutput, &job.SubmittedBy,
		&job.CreatedAt, &job.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// ListMergeJobs returns merge jobs for a workspace with optional filtering
// and cursor-based pagination. Jobs are ordered by (created_at ASC, id ASC).
// Returns the jobs and a next_cursor (empty if no more pages).
func ListMergeJobs(db *sql.DB, workspaceSlug string, opts ListOptions) ([]MergeJob, string, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	query := `SELECT
		id, nonce, campaign_id, spec_id, workspace_slug, target_branch, source_ref,
		status, rejection_reason, retry_count, available_at, base_sha, merged_sha,
		conflict_details, check_output, submitted_by, created_at, updated_at
	FROM merge_jobs
	WHERE workspace_slug = ?`
	args := []interface{}{workspaceSlug}

	if opts.Status != "" {
		query += " AND status = ?"
		args = append(args, opts.Status)
	}

	if opts.AfterID != "" && opts.AfterTime != "" {
		query += " AND (created_at > ? OR (created_at = ? AND id > ?))"
		args = append(args, opts.AfterTime, opts.AfterTime, opts.AfterID)
	}

	query += " ORDER BY created_at ASC, id ASC LIMIT ?"
	args = append(args, limit+1)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var jobs []MergeJob
	for rows.Next() {
		var job MergeJob
		if err := rows.Scan(
			&job.ID, &job.Nonce, &job.CampaignID, &job.SpecID,
			&job.WorkspaceSlug, &job.TargetBranch, &job.SourceRef,
			&job.Status, &job.RejectionReason, &job.RetryCount,
			&job.AvailableAt, &job.BaseSHA, &job.MergedSHA,
			&job.ConflictDetails, &job.CheckOutput, &job.SubmittedBy,
			&job.CreatedAt, &job.UpdatedAt,
		); err != nil {
			return nil, "", err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(jobs) > limit {
		jobs = jobs[:limit]
		nextCursor = jobs[limit-1].ID
	}

	return jobs, nextCursor, nil
}

// DirectUpdateStatus updates status and optional fields WITHOUT enforcing
// state machine rules. Used by internal queue operations like backoff.
func DirectUpdateStatus(db *sql.DB, id, status string, extraCols []string, extraVals []interface{}) error {
	now := apikit.NowUTC()

	setClause := "status = ?, updated_at = ?"
	args := []interface{}{status, now}
	for i, col := range extraCols {
		setClause += ", " + col + " = ?"
		args = append(args, extraVals[i])
	}
	args = append(args, id)

	query := "UPDATE merge_jobs SET " + setClause + " WHERE id = ?"
	result, err := db.Exec(query, args...)
	if err != nil {
		return err
	}
	rowsAff, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAff == 0 {
		return fmt.Errorf("merge job %s not found", id)
	}
	return nil
}

// ConditionalUpdateStatus updates status only if the current status matches
// expectedStatus. Returns the number of rows affected.
func ConditionalUpdateStatus(db *sql.DB, id, expectedStatus, newStatus string) (int64, error) {
	now := apikit.NowUTC()
	result, err := db.Exec(
		"UPDATE merge_jobs SET status = ?, updated_at = ? WHERE id = ? AND status = ?",
		newStatus, now, id, expectedStatus,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// isTerminal returns true if the status is a terminal state.
func isTerminal(status string) bool {
	switch status {
	case "merged", "conflict", "check_failed", "push_failed", "cancelled", "dead_letter":
		return true
	}
	return false
}
