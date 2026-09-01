package audit

import (
	"context"
	"database/sql"
)

// Store defines the audit storage interface for inserting and querying
// audit records. Concrete implementation is backed by DuckDB.
type Store interface {
	// InsertHubEvent inserts a prepared hub audit event row.
	InsertHubEvent(ctx context.Context, row HubEventRow) error
}

// NewStore creates a Store backed by the given DuckDB connection.
func NewStore(db *sql.DB) Store {
	panic("not implemented")
}
