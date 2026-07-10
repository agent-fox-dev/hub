package teams

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// timeFormat is the Go format string for RFC3339 UTC with microsecond precision.
// The trailing "Z" is a literal character (UTC zone designator).
const timeFormat = "2006-01-02T15:04:05.000000Z"

// timeFormats lists formats the store recognises when reading timestamps back
// from SQLite. The modernc.org/sqlite driver normalises DATETIME values and
// may strip trailing ".000000" when microseconds are zero, so we accept the
// full-precision format and several reduced-precision fallbacks.
var timeFormats = []string{
	"2006-01-02T15:04:05.000000Z",
	"2006-01-02T15:04:05.000000Z07:00",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02 15:04:05",
	time.RFC3339Nano,
	time.RFC3339,
}

// parseTimestamp tries multiple timestamp formats to parse a datetime string
// from SQLite. Returns the parsed time in UTC.
func parseTimestamp(s string) (time.Time, error) {
	for _, fmt := range timeFormats {
		if t, err := time.Parse(fmt, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse timestamp %q", s)
}

// Team represents a team record in the database.
type Team struct {
	ID        string
	Name      string
	Slug      string
	URL       *string // nullable
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TeamMember represents a team membership record joined with user data.
type TeamMember struct {
	UserID   string
	TeamID   string
	Email    string
	Name     string
	JoinedAt time.Time
}

// Store provides data access methods for teams and team_members.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store backed by the given database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// FormatTime formats a time.Time value as RFC3339 UTC with microsecond precision.
func FormatTime(t time.Time) string {
	return t.UTC().Format(timeFormat)
}

// CreateTeam inserts a new team within a transaction, checking for name/slug
// uniqueness among non-deleted teams at the application layer. If a concurrent
// insert triggers a database-level partial UNIQUE index violation, the error
// is mapped to the appropriate sentinel error.
func (s *Store) CreateTeam(name, slug string, urlVal *string) (*Team, error) {
	now := time.Now().UTC()
	id := uuid.New().String()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint: errcheck

	// Application-layer uniqueness check among non-deleted teams.
	var existingName string
	err = tx.QueryRow(
		`SELECT name FROM teams WHERE name = ? AND status != 'deleted'`, name,
	).Scan(&existingName)
	if err == nil {
		return nil, ErrTeamNameExists
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("checking name uniqueness: %w", err)
	}

	var existingSlug string
	err = tx.QueryRow(
		`SELECT slug FROM teams WHERE slug = ? AND status != 'deleted'`, slug,
	).Scan(&existingSlug)
	if err == nil {
		return nil, ErrTeamSlugExists
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("checking slug uniqueness: %w", err)
	}

	// Insert the new team.
	_, err = tx.Exec(
		`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', ?, ?)`,
		id, name, slug, urlVal, FormatTime(now), FormatTime(now),
	)
	if err != nil {
		return nil, mapConstraintError(err)
	}

	if err := tx.Commit(); err != nil {
		return nil, mapConstraintError(err)
	}

	return &Team{
		ID:        id,
		Name:      name,
		Slug:      slug,
		URL:       urlVal,
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// ListTeams retrieves teams from the database, filtered by status.
// If includeArchived is false, only active teams are returned.
// If includeArchived is true, both active and archived teams are returned.
// Deleted teams are never returned. Results are ordered by created_at ascending.
func (s *Store) ListTeams(includeArchived bool) ([]Team, error) {
	var query string
	if includeArchived {
		query = `SELECT id, name, slug, url, status, created_at, updated_at
		         FROM teams WHERE status IN ('active', 'archived')
		         ORDER BY created_at ASC`
	} else {
		query = `SELECT id, name, slug, url, status, created_at, updated_at
		         FROM teams WHERE status = 'active'
		         ORDER BY created_at ASC`
	}

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("listing teams: %w", err)
	}
	defer rows.Close()

	var result []Team
	for rows.Next() {
		var t Team
		var createdStr, updatedStr string
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.URL, &t.Status, &createdStr, &updatedStr); err != nil {
			return nil, fmt.Errorf("scanning team row: %w", err)
		}
		var parseErr error
		t.CreatedAt, parseErr = parseTimestamp(createdStr)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing created_at for team %s: %w", t.ID, parseErr)
		}
		t.UpdatedAt, parseErr = parseTimestamp(updatedStr)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing updated_at for team %s: %w", t.ID, parseErr)
		}
		result = append(result, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating team rows: %w", err)
	}

	return result, nil
}

// GetTeamByID retrieves a single team by its UUID. Returns ErrTeamNotFound
// if the team does not exist or has status = 'deleted'.
func (s *Store) GetTeamByID(id string) (*Team, error) {
	var t Team
	var createdStr, updatedStr string
	err := s.db.QueryRow(
		`SELECT id, name, slug, url, status, created_at, updated_at
		 FROM teams WHERE id = ? AND status != 'deleted'`, id,
	).Scan(&t.ID, &t.Name, &t.Slug, &t.URL, &t.Status, &createdStr, &updatedStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrTeamNotFound
		}
		return nil, fmt.Errorf("getting team by ID: %w", err)
	}

	var parseErr error
	t.CreatedAt, parseErr = parseTimestamp(createdStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parsing created_at for team %s: %w", t.ID, parseErr)
	}
	t.UpdatedAt, parseErr = parseTimestamp(updatedStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parsing updated_at for team %s: %w", t.ID, parseErr)
	}

	return &t, nil
}

// UpdateTeamStatus transitions a team's status and updates the updated_at
// timestamp. Returns ErrTeamNotFound if the team does not exist or is deleted.
// Callers are responsible for validating the transition is valid before calling.
func (s *Store) UpdateTeamStatus(id, newStatus string) (*Team, error) {
	now := time.Now().UTC()
	nowStr := FormatTime(now)

	result, err := s.db.Exec(
		`UPDATE teams SET status = ?, updated_at = ? WHERE id = ? AND status != 'deleted'`,
		newStatus, nowStr, id,
	)
	if err != nil {
		return nil, fmt.Errorf("updating team status: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("checking rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return nil, ErrTeamNotFound
	}

	// Read back the updated row.
	return s.GetTeamByID(id)
}

// DeleteTeam permanently removes an archived team and all its members within
// a single transaction. The caller must verify the team is archived before
// calling. Returns ErrTeamNotFound if the team does not exist or is not archived.
// Returns ErrArchiveBeforeDelete if the team is active.
func (s *Store) DeleteTeam(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint: errcheck

	// Fetch team status within the transaction.
	var status string
	err = tx.QueryRow(`SELECT status FROM teams WHERE id = ?`, id).Scan(&status)
	if err != nil {
		if err == sql.ErrNoRows {
			return ErrTeamNotFound
		}
		return fmt.Errorf("fetching team for delete: %w", err)
	}

	if status == "deleted" {
		return ErrTeamNotFound
	}
	if status == "active" {
		return ErrArchiveBeforeDelete
	}

	// Delete team_members rows first (also handled by CASCADE, but explicit
	// for spec compliance and transaction atomicity verification).
	if _, err := tx.Exec(`DELETE FROM team_members WHERE team_id = ?`, id); err != nil {
		return fmt.Errorf("deleting team members: %w", err)
	}

	// Delete the team row.
	if _, err := tx.Exec(`DELETE FROM teams WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting team: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing delete transaction: %w", err)
	}

	return nil
}

// UserExists checks whether a user with the given ID exists in the users table.
func (s *Store) UserExists(id string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT count(*) FROM users WHERE id = ?`, id).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking user existence: %w", err)
	}
	return count > 0, nil
}

// AddMember adds a user to a team. The operation is idempotent: if the
// membership already exists, no mutation occurs. In both cases the method
// performs a fresh JOIN against the users table to return current user data
// (email, name) and the original joined_at timestamp.
//
// Callers must verify team existence, team status, and user existence
// before calling this method.
func (s *Store) AddMember(teamID, userID string) (*TeamMember, error) {
	now := time.Now().UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint: errcheck

	// Check if membership already exists within the transaction.
	var existingCreatedAt string
	err = tx.QueryRow(
		`SELECT created_at FROM team_members WHERE team_id = ? AND user_id = ?`,
		teamID, userID,
	).Scan(&existingCreatedAt)

	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("checking membership: %w", err)
	}

	if err == sql.ErrNoRows {
		// Insert new membership.
		_, insertErr := tx.Exec(
			`INSERT INTO team_members (team_id, user_id, created_at) VALUES (?, ?, ?)`,
			teamID, userID, FormatTime(now),
		)
		if insertErr != nil {
			// Handle composite PK violation (concurrent race).
			if strings.Contains(insertErr.Error(), "UNIQUE constraint failed") ||
				strings.Contains(insertErr.Error(), "PRIMARY KEY") {
				// Another request beat us — treat as idempotent success.
				// Fall through to the fresh JOIN below after commit.
			} else {
				return nil, fmt.Errorf("inserting team member: %w", insertErr)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing add member transaction: %w", err)
	}

	// Perform a fresh JOIN against users to retrieve current email and name,
	// plus the original joined_at timestamp from team_members.created_at.
	return s.getMemberWithUserData(teamID, userID)
}

// getMemberWithUserData retrieves a team member record joined with current
// user data (email, full_name) from the users table.
func (s *Store) getMemberWithUserData(teamID, userID string) (*TeamMember, error) {
	var m TeamMember
	var joinedStr string

	err := s.db.QueryRow(
		`SELECT tm.team_id, tm.user_id, u.email, u.full_name, tm.created_at
		 FROM team_members tm
		 JOIN users u ON u.id = tm.user_id
		 WHERE tm.team_id = ? AND tm.user_id = ?`,
		teamID, userID,
	).Scan(&m.TeamID, &m.UserID, &m.Email, &m.Name, &joinedStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("member not found after add: team=%s user=%s", teamID, userID)
		}
		return nil, fmt.Errorf("reading member data: %w", err)
	}

	var parseErr error
	m.JoinedAt, parseErr = parseTimestamp(joinedStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parsing joined_at: %w", parseErr)
	}

	return &m, nil
}

// ListMembers retrieves all members of a team, joined with current user data,
// ordered by joined_at (team_members.created_at) ascending.
func (s *Store) ListMembers(teamID string) ([]TeamMember, error) {
	rows, err := s.db.Query(
		`SELECT tm.team_id, tm.user_id, u.email, u.full_name, tm.created_at
		 FROM team_members tm
		 JOIN users u ON u.id = tm.user_id
		 WHERE tm.team_id = ?
		 ORDER BY tm.created_at ASC`,
		teamID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing members: %w", err)
	}
	defer rows.Close()

	var result []TeamMember
	for rows.Next() {
		var m TeamMember
		var joinedStr string
		if err := rows.Scan(&m.TeamID, &m.UserID, &m.Email, &m.Name, &joinedStr); err != nil {
			return nil, fmt.Errorf("scanning member row: %w", err)
		}
		var parseErr error
		m.JoinedAt, parseErr = parseTimestamp(joinedStr)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing joined_at: %w", parseErr)
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating member rows: %w", err)
	}

	return result, nil
}

// mapConstraintError inspects a SQLite error for partial UNIQUE index
// violations and maps them to the appropriate sentinel error.
func mapConstraintError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "unique constraint") {
		if strings.Contains(msg, "teams.name") || strings.Contains(msg, "idx_teams_name") {
			return ErrTeamNameExists
		}
		if strings.Contains(msg, "teams.slug") || strings.Contains(msg, "idx_teams_slug") {
			return ErrTeamSlugExists
		}
		// Generic constraint error — try name first as fallback.
		return ErrTeamNameExists
	}
	return fmt.Errorf("database error: %w", err)
}
