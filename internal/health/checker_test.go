package health

import (
	"database/sql"
	"testing"

	"github.com/txsvc/apikit"
	_ "modernc.org/sqlite"
)

// TestNewDBChecker_HealthyDB verifies that the health checker returns nil
// when the database connection is alive and healthy.
func TestNewDBChecker_HealthyDB(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() returned error: %v", err)
	}
	defer sqlDB.Close()

	db := &apikit.DB{SqlDB: sqlDB}
	checker := NewDBChecker(db)
	if err := checker(); err != nil {
		t.Errorf("checker() returned error on healthy DB: %v; want nil", err)
	}
}

// TestNewDBChecker_UnreachableDB verifies that the health checker returns a
// non-nil error when the database connection is closed/unreachable.
func TestNewDBChecker_UnreachableDB(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() returned error: %v", err)
	}
	sqlDB.Close() // close immediately to simulate an unreachable DB

	db := &apikit.DB{SqlDB: sqlDB}
	checker := NewDBChecker(db)
	if err := checker(); err == nil {
		t.Error("checker() returned nil on closed DB; want non-nil error")
	}
}
