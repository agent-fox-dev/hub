package audit

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	_ "github.com/marcboeker/go-duckdb" // register "duckdb" driver
)

// OpenDB calls os.MkdirAll on the parent directory of path, then opens
// a DuckDB connection using the duckdb-go driver with path as the DSN.
// Returns (*sql.DB, nil) on success, or (nil, error) on failure.
func OpenDB(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("audit: database path must not be empty")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	return sql.Open("duckdb", path)
}
