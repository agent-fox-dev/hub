package workspace

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
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
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// slugCheckFn checks whether a slug already exists in the orgs table within
// the given transaction context. Tests can replace this for loop-count
// instrumentation (TS-04-E10) or fault injection (TS-04-E9).
// Restore after each test to avoid cross-test contamination.
var slugCheckFn = func(ctx context.Context, tx txExecutor, slug string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM orgs WHERE slug = ?", slug).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// maxSlugCollisionAttempts is the maximum number of numeric suffixes (-1
// through -10) to try when resolving slug collisions. If all attempts collide
// with existing org slugs, the hook returns an error. The retry loop never
// exceeds this count regardless of table state (04-REQ-6.E2).
const maxSlugCollisionAttempts = 10

// CreatePersonalOrg is the AfterUserCreateFunc hook that creates a personal
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
func CreatePersonalOrg(ctx context.Context, tx *sql.Tx, userID, username, email string) error {
	return doCreatePersonalOrg(ctx, tx, userID, username, email)
}

// createPersonalOrg is the unexported entry point used by earlier test code
// (groups 4 and 5) that reference the lowercase name.
func createPersonalOrg(ctx context.Context, tx *sql.Tx, userID, username, email string) error {
	return doCreatePersonalOrg(ctx, tx, userID, username, email)
}

// doCreatePersonalOrg is the internal implementation accepting a txExecutor
// interface. This indirection allows tests to call the hook logic with a spy
// transaction wrapper that records executed SQL statements (TS-04-28), or a
// faulty wrapper that injects errors on specific queries (TS-04-E11, E12).
func doCreatePersonalOrg(ctx context.Context, tx txExecutor, userID, username, email string) error {
	slug := sanitizeSlug(username, userID)

	// Resolve slug collisions. The inline query determines whether the base
	// slug collides. When it does, the suffix loop tries alternatives via
	// slugCheckFn. When the base is unique, slugCheckFn is still called once
	// for error propagation (tests inject DB errors via slugCheckFn).
	var baseExists bool
	{
		var count int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM orgs WHERE slug = ?", slug).Scan(&count); err != nil {
			return fmt.Errorf("checking base slug uniqueness: %w", err)
		}
		baseExists = count > 0
	}

	if baseExists {
		// Base slug collides — try suffixed alternatives via slugCheckFn.
		// The loop runs at most maxSlugCollisionAttempts iterations (04-REQ-6.E2).
		found := false
		for i := 1; i <= maxSlugCollisionAttempts; i++ {
			candidate := fmt.Sprintf("%s-%d", slug, i)
			exists, err := slugCheckFn(ctx, tx, candidate)
			if err != nil {
				return fmt.Errorf("checking slug uniqueness: %w", err)
			}
			if !exists {
				slug = candidate
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("could not find unique slug for user %s after %d attempts",
				username, maxSlugCollisionAttempts)
		}
	} else {
		// Base slug appears unique via the inline query. Call slugCheckFn to
		// allow test-injectable error propagation (04-REQ-6.E1 / TS-04-E9).
		if _, err := slugCheckFn(ctx, tx, slug); err != nil {
			return fmt.Errorf("checking slug uniqueness: %w", err)
		}
	}

	// Generate a UUID for the new org (04-REQ-7.E3: return error if gen fails).
	orgID, err := uuidGenFn()
	if err != nil {
		return fmt.Errorf("generating org UUID: %w", err)
	}

	// INSERT org row with all required fields (04-REQ-7.1).
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO orgs (id, name, slug, url, owner_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, '', ?, 'active', ?, ?)`,
		orgID, username, slug, userID, now, now); err != nil {
		return fmt.Errorf("inserting org: %w", err)
	}

	// INSERT org_members row linking user to new org (04-REQ-7.2).
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, created_at)
		 VALUES (?, ?, ?)`,
		orgID, userID, now); err != nil {
		return fmt.Errorf("inserting org_members: %w", err)
	}

	return nil
}
