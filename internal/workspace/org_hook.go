package workspace

import (
	"context"
	"database/sql"
	"fmt"
)

// txExecutor abstracts the subset of *sql.Tx methods used by the personal org
// hook. *sql.Tx satisfies this interface. Tests can inject a recording spy to
// verify that all INSERT statements execute on the passed transaction, not on
// a separate database connection (TS-04-28).
type txExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// uuidGenFn generates a new UUID string for org IDs.
// Tests can replace this to inject UUID generation failures (TS-04-E13).
// Restore after each test to avoid cross-test contamination.
var uuidGenFn = func() (string, error) {
	// Stub: implementation will be provided in task group 10.
	return "", fmt.Errorf("UUID generation not implemented")
}

// slugCheckFn checks whether a slug already exists in the orgs table within
// the given transaction context. Tests can replace this for loop-count
// instrumentation (TS-04-E10) or fault injection (TS-04-E9).
// Restore after each test to avoid cross-test contamination.
var slugCheckFn = func(ctx context.Context, tx txExecutor, slug string) (bool, error) {
	// Stub: implementation will be provided in task group 10.
	return false, fmt.Errorf("slugCheck not implemented")
}

// maxSlugCollisionAttempts is the maximum number of numeric suffixes (-1
// through -10) to try when resolving slug collisions. If all attempts collide
// with existing org slugs, the hook returns an error. The retry loop never
// exceeds this count regardless of table state (04-REQ-6.E2).
const maxSlugCollisionAttempts = 10

// createPersonalOrg is the AfterUserCreateFunc hook that creates a personal
// organization and org_members row for a newly created user. It:
//  1. Sanitizes the username into a valid org slug via sanitizeSlug
//  2. Resolves slug collisions by appending -1 through -10
//  3. Generates a UUID for the new org
//  4. Inserts a row into orgs with name=username, slug=unique, owner_id=userID,
//     status="active", url="", and timestamps in RFC 3339 format
//  5. Inserts a row into org_members linking the new user to the new org
//
// All database operations use the passed *sql.Tx for atomicity with the
// enclosing user creation transaction.
// Implementation: task group 10.
func createPersonalOrg(ctx context.Context, tx *sql.Tx, userID, username, email string) error {
	return doCreatePersonalOrg(ctx, tx, userID, username, email)
}

// doCreatePersonalOrg is the internal implementation accepting a txExecutor
// interface. This indirection allows tests to call the hook logic with a spy
// transaction wrapper that records executed SQL statements (TS-04-28), or a
// faulty wrapper that injects errors on specific queries (TS-04-E11, E12).
// Implementation: task group 10.
func doCreatePersonalOrg(ctx context.Context, tx txExecutor, userID, username, email string) error {
	// Stub: return error so tests fail.
	return fmt.Errorf("createPersonalOrg not implemented")
}
