package workspace

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openFullTestDB opens an in-memory SQLite database with users, orgs, and
// org_members tables for property tests that simulate the complete user
// creation flow (user INSERT + hook call in a single transaction).
func openFullTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT NOT NULL PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			full_name TEXT,
			role TEXT NOT NULL DEFAULT 'user',
			status TEXT NOT NULL DEFAULT 'active',
			provider TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE (provider, provider_id)
		)`,
		`CREATE TABLE IF NOT EXISTS orgs (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			url TEXT,
			owner_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS org_members (
			org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (org_id, user_id)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema init: %v", err)
		}
	}
	return db
}

// ---------------------------------------------------------------------------
// TS-04-P1: Property test — for any user creation attempt, after the
//           operation completes, either both the user row and a personal org
//           row exist (with matching owner_id) or neither exists.
// Property: 04-PROP-1
// Validates: 04-REQ-2.1, 04-REQ-2.2, 04-REQ-3.1, 04-REQ-3.3, 04-REQ-7.1,
//            04-REQ-7.E1, 04-REQ-7.E2
// ---------------------------------------------------------------------------
func TestOrgHook_PropertyAtomicity(t *testing.T) {
	const iterations = 50
	rng := rand.New(rand.NewSource(42))

	db := openFullTestDB(t)

	successCount := 0

	for i := 0; i < iterations; i++ {
		userID := fmt.Sprintf("user-p1-%04d", i)
		username := fmt.Sprintf("propuser%d", i)
		email := fmt.Sprintf("p%d@test.com", i)

		// Randomly inject hook failure ~50% of the time by replacing
		// uuidGenFn with a failing function.
		shouldFail := rng.Intn(2) == 0

		origUUID := uuidGenFn
		if shouldFail {
			uuidGenFn = func() (string, error) {
				return "", fmt.Errorf("injected failure for atomicity test")
			}
		}

		// Simulate user creation flow: begin tx, insert user, call hook.
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			uuidGenFn = origUUID
			t.Fatalf("iteration %d: BeginTx: %v", i, err)
		}

		now := time.Now().UTC().Format(time.RFC3339)
		_, insertErr := tx.Exec(
			`INSERT INTO users (id, username, email, role, status, provider, provider_id, created_at, updated_at)
			 VALUES (?, ?, ?, 'user', 'active', 'test', ?, ?, ?)`,
			userID, username, email, userID, now, now,
		)

		var hookErr error
		if insertErr == nil {
			hookErr = createPersonalOrg(context.Background(), tx, userID, username, email)
		}

		// Commit if everything succeeded; rollback otherwise.
		if insertErr == nil && hookErr == nil {
			if commitErr := tx.Commit(); commitErr != nil {
				tx.Rollback()
			}
		} else {
			tx.Rollback()
		}

		uuidGenFn = origUUID

		// Invariant: either BOTH user and org exist, or NEITHER exists.
		var userCount, orgCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE id = ?", userID).Scan(&userCount); err != nil {
			t.Fatalf("iteration %d: user count query: %v", i, err)
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM orgs WHERE owner_id = ?", userID).Scan(&orgCount); err != nil {
			t.Fatalf("iteration %d: org count query: %v", i, err)
		}

		userExists := userCount > 0
		orgExists := orgCount > 0

		if userExists != orgExists {
			t.Errorf("iteration %d (shouldFail=%v): user exists=%v, org exists=%v; atomicity violated",
				i, shouldFail, userExists, orgExists)
		}

		if userExists && orgExists {
			successCount++
		}
	}

	// With ~50% failure injection, at least some attempts should succeed
	// when the implementation is correct. With the stub always failing,
	// this check will fail — which is the expected behavior for a group-1
	// failing test.
	if successCount == 0 {
		t.Errorf("no successful user+org creations in %d attempts; expected ~%d successes",
			iterations, iterations/2)
	}
}

// ---------------------------------------------------------------------------
// TS-04-P2: Property test — for any call to the hub personal org hook, the
//           slug written to the orgs table does not duplicate any pre-existing
//           slug.
// Property: 04-PROP-2
// Validates: 04-REQ-6.1, 04-REQ-6.2, 04-REQ-6.3
// ---------------------------------------------------------------------------
func TestOrgHook_PropertySlugUniqueness(t *testing.T) {
	type testCase struct {
		name     string
		existing []string // pre-existing org slugs to seed
		username string
	}

	cases := []testCase{
		{"no_collisions", nil, "unique-user"},
		{"one_collision_base", []string{"alice"}, "alice"},
		{"two_collisions", []string{"alice", "alice-1"}, "alice"},
		{"five_collisions", []string{"alice", "alice-1", "alice-2", "alice-3", "alice-4"}, "alice"},
		{"nine_collisions", []string{
			"alice", "alice-1", "alice-2", "alice-3", "alice-4",
			"alice-5", "alice-6", "alice-7", "alice-8"}, "alice"},
		{"ten_collisions", []string{
			"alice", "alice-1", "alice-2", "alice-3", "alice-4",
			"alice-5", "alice-6", "alice-7", "alice-8", "alice-9"}, "alice"},
		{"all_eleven_collide", []string{
			"alice", "alice-1", "alice-2", "alice-3", "alice-4",
			"alice-5", "alice-6", "alice-7", "alice-8", "alice-9", "alice-10"}, "alice"},
		{"different_username_no_collision", []string{"alice"}, "bob"},
		{"mixed_pre_existing", []string{"alice", "bob", "charlie"}, "alice"},
		{"fifteen_pre_existing", []string{
			"dave", "dave-1", "dave-2", "dave-3", "dave-4", "dave-5",
			"dave-6", "dave-7", "dave-8", "dave-9", "dave-10",
			"dave-11", "dave-12", "dave-13", "dave-14"}, "dave"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openOrgTestDB(t)
			for _, slug := range tc.existing {
				seedOrgSlug(t, db, slug)
			}

			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}

			userID := "user-p2-" + tc.name
			hookErr := createPersonalOrg(context.Background(), tx, userID, tc.username, "p@test.com")

			if hookErr != nil {
				tx.Rollback()

				// Hook returned error — verify no new org exists.
				var count int
				if err := db.QueryRow("SELECT COUNT(*) FROM orgs WHERE owner_id = ?", userID).Scan(&count); err != nil {
					t.Fatalf("org count query: %v", err)
				}
				if count != 0 {
					t.Errorf("hook returned error but org row exists for %s", userID)
				}

				// If fewer than 11 pre-existing slugs, the hook should have
				// found a unique slug. Error indicates broken implementation.
				if len(tc.existing) < 11 {
					t.Errorf("hook returned error %v with %d pre-existing slugs; expected success",
						hookErr, len(tc.existing))
				}
				return
			}

			if err := tx.Commit(); err != nil {
				t.Fatalf("Commit: %v", err)
			}

			// Hook succeeded — verify the inserted slug is unique in the table.
			var insertedSlug string
			if err := db.QueryRow("SELECT slug FROM orgs WHERE owner_id = ?", userID).Scan(&insertedSlug); err != nil {
				t.Fatalf("query inserted slug: %v", err)
			}

			var slugCount int
			if err := db.QueryRow("SELECT COUNT(*) FROM orgs WHERE slug = ?", insertedSlug).Scan(&slugCount); err != nil {
				t.Fatalf("slug count query: %v", err)
			}
			if slugCount != 1 {
				t.Errorf("slug %q appears %d times in orgs; want exactly 1 (unique)", insertedSlug, slugCount)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-04-P6: Property test — the slug collision retry loop always terminates
//           after at most 10 iterations, regardless of how many slugs exist
//           in the orgs table.
// Property: 04-PROP-6
// Validates: 04-REQ-6.1, 04-REQ-6.2, 04-REQ-6.E2
// ---------------------------------------------------------------------------
func TestOrgHook_PropertyLoopBounded(t *testing.T) {
	// Verify the constant matches the spec.
	if maxSlugCollisionAttempts != 10 {
		t.Fatalf("maxSlugCollisionAttempts = %d; want 10", maxSlugCollisionAttempts)
	}

	// Test with varying numbers of pre-existing colliding slugs (0 to 20).
	// The hook tries the base slug + suffixes -1 through -10 (11 candidates).
	// With >= 11 pre-existing colliding slugs, the hook should return error.
	for numExisting := 0; numExisting <= 20; numExisting++ {
		t.Run(fmt.Sprintf("existing_%d", numExisting), func(t *testing.T) {
			db := openOrgTestDB(t)

			baseSlug := "looptest"
			// Seed the base slug and suffix slugs as needed.
			if numExisting > 0 {
				seedOrgSlug(t, db, baseSlug)
			}
			for i := 1; i < numExisting; i++ {
				seedOrgSlug(t, db, fmt.Sprintf("%s-%d", baseSlug, i))
			}

			// Instrument slug check to count iterations and query real DB.
			iterationCount := 0
			origCheck := slugCheckFn
			slugCheckFn = func(ctx context.Context, tx txExecutor, slug string) (bool, error) {
				iterationCount++
				var count int
				row := tx.QueryRowContext(ctx,
					"SELECT COUNT(*) FROM orgs WHERE slug = ?", slug)
				if err := row.Scan(&count); err != nil {
					return false, err
				}
				return count > 0, nil
			}
			t.Cleanup(func() { slugCheckFn = origCheck })

			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			defer tx.Rollback()

			userID := fmt.Sprintf("user-p6-%d", numExisting)
			hookErr := createPersonalOrg(context.Background(), tx, userID, baseSlug, "p6@test.com")

			// The loop must NEVER exceed maxSlugCollisionAttempts iterations.
			if iterationCount > maxSlugCollisionAttempts {
				t.Errorf("iteration count = %d; exceeds cap of %d",
					iterationCount, maxSlugCollisionAttempts)
			}

			// When all candidates collide (base + 10 suffixes = 11 total),
			// the hook must return an error.
			if numExisting >= 11 {
				if hookErr == nil {
					t.Errorf("hook returned nil with %d colliding slugs; want error", numExisting)
				}
			} else {
				// When a unique slug is available, hook should succeed.
				if hookErr != nil {
					t.Errorf("hook returned error %v with %d colliding slugs; want success",
						hookErr, numExisting)
				}
			}
		})
	}
}
