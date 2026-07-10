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
