package health

import (
	"context"

	"github.com/txsvc/apikit"
)

// NewDBChecker returns an apikit.HealthChecker that verifies database
// connectivity by pinging the given database connection.
//
// apikit.HealthChecker is a function type (func() error), not an interface.
// This factory function creates a closure that calls db.Ping on each
// invocation, suitable for passing to apikit.NewServer.
func NewDBChecker(db *apikit.DB) apikit.HealthChecker {
	return func() error {
		return db.Ping(context.Background())
	}
}
