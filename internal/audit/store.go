package audit

import (
	"context"
	"database/sql"
	"errors"
)

// Store defines the audit storage interface for inserting and querying
// audit records, session lifecycle, and token usage operations. The concrete
// implementation is backed by DuckDB.
type Store interface {
	// InsertHubEvent inserts a prepared hub audit event row.
	InsertHubEvent(ctx context.Context, row HubEventRow) error

	// CreateSession inserts a new agent_sessions row. If a session with the
	// same ID already exists, it returns the existing session and false.
	// Returns the session, true if created, and any error.
	CreateSession(ctx context.Context, s *Session) (*Session, bool, error)

	// GetSession retrieves a session by ID. Returns ErrSessionNotFound if
	// no session with the given ID exists.
	GetSession(ctx context.Context, id string) (*Session, error)

	// CompleteSession atomically transitions an active session to a terminal
	// state. Returns ErrSessionNotActive if the session is not active, and
	// ErrSessionNotFound if the session does not exist.
	CompleteSession(ctx context.Context, id string, req *CompleteSessionRequest) (*Session, error)

	// InsertTokenUsage creates a new token_usage record linked to a session.
	// If a record with the same ID already exists, returns the existing record.
	InsertTokenUsage(ctx context.Context, u *TokenUsage) (*TokenUsage, error)

	// ListSessions returns a paginated list of sessions matching the given
	// parameters, with token_summary aggregated from token_usage rows.
	ListSessions(ctx context.Context, params SessionListParams, accessibleWorkspaces []string) (*SessionListResponse, error)

	// GetSessionWithSummary retrieves a session by ID with its aggregated
	// token_summary. Returns ErrSessionNotFound if no session exists.
	GetSessionWithSummary(ctx context.Context, id string) (*Session, error)

	// ListTokenUsage returns a paginated list of token_usage records for
	// a session, plus unbounded totals across all records.
	ListTokenUsage(ctx context.Context, sessionID string, params UsageListParams) (*UsageListResponse, error)
}

// NewStore creates a Store backed by the given DuckDB connection.
func NewStore(db *sql.DB) Store {
	return &duckDBStore{db: db}
}

type duckDBStore struct {
	db *sql.DB
}

func (s *duckDBStore) InsertHubEvent(_ context.Context, _ HubEventRow) error {
	return errors.New("not implemented")
}

func (s *duckDBStore) CreateSession(_ context.Context, _ *Session) (*Session, bool, error) {
	return nil, false, errors.New("not implemented")
}

func (s *duckDBStore) GetSession(_ context.Context, _ string) (*Session, error) {
	return nil, errors.New("not implemented")
}

func (s *duckDBStore) CompleteSession(_ context.Context, _ string, _ *CompleteSessionRequest) (*Session, error) {
	return nil, errors.New("not implemented")
}

func (s *duckDBStore) InsertTokenUsage(_ context.Context, _ *TokenUsage) (*TokenUsage, error) {
	return nil, errors.New("not implemented")
}

func (s *duckDBStore) ListSessions(_ context.Context, _ SessionListParams, _ []string) (*SessionListResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *duckDBStore) GetSessionWithSummary(_ context.Context, _ string) (*Session, error) {
	return nil, errors.New("not implemented")
}

func (s *duckDBStore) ListTokenUsage(_ context.Context, _ string, _ UsageListParams) (*UsageListResponse, error) {
	return nil, errors.New("not implemented")
}
