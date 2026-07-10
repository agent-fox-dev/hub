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
