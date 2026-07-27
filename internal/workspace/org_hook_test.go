package workspace

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test helpers for org hook tests
// ---------------------------------------------------------------------------

// openOrgTestDB opens an in-memory SQLite database with the orgs and
// org_members tables. The orgs table includes the owner_id column added by
// spec 04 (not yet present in the current apikit schema). This is separate
// from openTestDB because the org schema here includes owner_id.
func openOrgTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	stmts := []string{
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

// seedOrgSlug inserts a minimal org row with the given slug for test setup.
// Uses unique id and name values derived from the slug to avoid UNIQUE
// constraint violations between seeded rows.
func seedOrgSlug(t *testing.T, db *sql.DB, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO orgs (id, name, slug, url, status, created_at, updated_at)
		 VALUES (?, ?, ?, '', 'active', ?, ?)`,
		"seed-id-"+slug, "seed-name-"+slug, slug, now, now,
	)
	if err != nil {
		t.Fatalf("seedOrgSlug(%q): %v", slug, err)
	}
}

// spyTx wraps a real *sql.Tx (via txExecutor) and records all ExecContext
// calls. Used by TS-04-28 to verify all INSERT statements go through the
// passed transaction, not through a separate database connection.
type spyTx struct {
	wrapped   txExecutor
	execCalls []string // recorded query strings
}

func newSpyTx(tx *sql.Tx) *spyTx {
	return &spyTx{wrapped: tx}
}

func (s *spyTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	s.execCalls = append(s.execCalls, query)
	return s.wrapped.ExecContext(ctx, query, args...)
}

func (s *spyTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.wrapped.QueryRowContext(ctx, query, args...)
}

// countInserts returns the number of ExecContext calls whose query starts
// with "INSERT" (case-insensitive).
func (s *spyTx) countInserts() int {
	n := 0
	for _, q := range s.execCalls {
		if strings.HasPrefix(strings.TrimSpace(strings.ToUpper(q)), "INSERT") {
			n++
		}
	}
	return n
}

// hasStatement returns true if any recorded query contains the given
// substring (case-insensitive).
func (s *spyTx) hasStatement(substr string) bool {
	upper := strings.ToUpper(substr)
	for _, q := range s.execCalls {
		if strings.Contains(strings.ToUpper(q), upper) {
			return true
		}
	}
	return false
}

// faultyTx wraps a txExecutor and returns a simulated error when a query
// matches errorOnSubstr. Used to test error paths in INSERT statements
// (TS-04-E11, TS-04-E12).
type faultyTx struct {
	wrapped       txExecutor
	errorOnSubstr string
}

func (f *faultyTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(strings.ToUpper(query), strings.ToUpper(f.errorOnSubstr)) {
		return nil, fmt.Errorf("simulated error on: %s", f.errorOnSubstr)
	}
	return f.wrapped.ExecContext(ctx, query, args...)
}

func (f *faultyTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return f.wrapped.QueryRowContext(ctx, query, args...)
}

// ---------------------------------------------------------------------------
// TS-04-23: Verify that slug collision resolution appends -1, -2, etc. until
//           a unique slug is found.
// Requirement: 04-REQ-6.1
// ---------------------------------------------------------------------------
func TestSlugCollision_AppendsNumericSuffix(t *testing.T) {
	db := openOrgTestDB(t)

	// Pre-populate orgs with slugs 'alice' and 'alice-1'.
	seedOrgSlug(t, db, "alice")
	seedOrgSlug(t, db, "alice-1")

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	err = createPersonalOrg(context.Background(), tx, "user-001", "alice", "alice@test.com")
	if err != nil {
		t.Fatalf("createPersonalOrg returned error: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Verify the hook inserted an org with slug 'alice-2'.
	var slug string
	if err := db.QueryRow("SELECT slug FROM orgs WHERE owner_id = ?", "user-001").Scan(&slug); err != nil {
		t.Fatalf("query org by owner_id: %v", err)
	}
	if slug != "alice-2" {
		t.Errorf("org slug = %q; want %q", slug, "alice-2")
	}
}

// ---------------------------------------------------------------------------
// TS-04-24: Verify that when all 10 slug suffix attempts collide, the hook
//           returns an error causing transaction rollback and HTTP 500.
// Requirement: 04-REQ-6.2
// ---------------------------------------------------------------------------
func TestSlugCollision_AllTenAttemptsExhausted(t *testing.T) {
	db := openOrgTestDB(t)

	// Pre-populate slugs: 'alice', 'alice-1', ..., 'alice-10' (11 total).
	// This exhausts the base slug plus all 10 suffixed candidates.
	seedOrgSlug(t, db, "alice")
	for i := 1; i <= 10; i++ {
		seedOrgSlug(t, db, fmt.Sprintf("alice-%d", i))
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	err = createPersonalOrg(context.Background(), tx, "user-002", "alice", "e@test.com")
	if err == nil {
		t.Fatal("createPersonalOrg returned nil error; want non-nil when all slug attempts collide")
	}

	// Explicitly rollback to free the connection for verification queries.
	tx.Rollback()

	// Verify no org was created for this user.
	var orgCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM orgs WHERE owner_id = ?", "user-002").Scan(&orgCount); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if orgCount != 0 {
		t.Errorf("org count for user-002 = %d; want 0 (all attempts should have been exhausted)", orgCount)
	}
}

// ---------------------------------------------------------------------------
// TS-04-25: Verify that when the candidate slug does not collide, it is used
//           immediately without any suffix.
// Requirement: 04-REQ-6.3
// ---------------------------------------------------------------------------
func TestSlugCollision_NoCollisionUsesImmediately(t *testing.T) {
	db := openOrgTestDB(t)

	// No pre-existing org with slug 'newuser'.

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	err = createPersonalOrg(context.Background(), tx, "user-003", "newuser", "n@test.com")
	if err != nil {
		t.Fatalf("createPersonalOrg returned error: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Verify the hook used 'newuser' directly (no suffix).
	var slug string
	if err := db.QueryRow("SELECT slug FROM orgs WHERE owner_id = ?", "user-003").Scan(&slug); err != nil {
		t.Fatalf("query org by owner_id: %v", err)
	}
	if slug != "newuser" {
		t.Errorf("org slug = %q; want %q (no suffix should be appended)", slug, "newuser")
	}
}

// ---------------------------------------------------------------------------
// TS-04-E9: Verify that a database error during slug uniqueness query causes
//           the hook to return an error and roll back the transaction.
// Requirement: 04-REQ-6.E1
// ---------------------------------------------------------------------------
func TestSlugCollision_DBErrorOnSlugCheck(t *testing.T) {
	db := openOrgTestDB(t)

	// Replace slugCheckFn to simulate a database error on the slug query.
	origCheck := slugCheckFn
	slugCheckFn = func(_ context.Context, _ txExecutor, _ string) (bool, error) {
		return false, fmt.Errorf("simulated database error on slug check")
	}
	t.Cleanup(func() { slugCheckFn = origCheck })

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	err = createPersonalOrg(context.Background(), tx, "user-200", "queryerr", "q@test.com")
	if err == nil {
		t.Fatal("createPersonalOrg returned nil error; want non-nil on slug check DB error")
	}

	// Explicitly rollback to free the connection for verification queries.
	tx.Rollback()

	// Verify no org or org_members rows were created.
	var orgCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM orgs WHERE owner_id = ?", "user-200").Scan(&orgCount); err != nil {
		t.Fatalf("org count query: %v", err)
	}
	if orgCount != 0 {
		t.Errorf("org count = %d; want 0", orgCount)
	}

	var memberCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM org_members WHERE user_id = ?", "user-200").Scan(&memberCount); err != nil {
		t.Fatalf("member count query: %v", err)
	}
	if memberCount != 0 {
		t.Errorf("member count = %d; want 0", memberCount)
	}
}

// ---------------------------------------------------------------------------
// TS-04-E10: Verify that the slug collision retry loop is capped at exactly
//            10 iterations regardless of table state.
// Requirement: 04-REQ-6.E2
// ---------------------------------------------------------------------------
func TestSlugCollision_LoopCappedAtTenIterations(t *testing.T) {
	db := openOrgTestDB(t)

	// Pre-populate slugs to ensure all would collide.
	seedOrgSlug(t, db, "testslug")
	for i := 1; i <= 20; i++ {
		seedOrgSlug(t, db, fmt.Sprintf("testslug-%d", i))
	}

	// Instrument slug check with a counter that always returns "exists".
	iterationCount := 0
	origCheck := slugCheckFn
	slugCheckFn = func(_ context.Context, _ txExecutor, _ string) (bool, error) {
		iterationCount++
		return true, nil // slug always exists
	}
	t.Cleanup(func() { slugCheckFn = origCheck })

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	err = createPersonalOrg(context.Background(), tx, "user-300", "testslug", "t@test.com")
	if err == nil {
		t.Fatal("createPersonalOrg returned nil; want error when all slugs collide")
	}

	// The retry loop must execute exactly maxSlugCollisionAttempts iterations.
	// slugCheckFn is called once per iteration of the collision resolution loop.
	if iterationCount != maxSlugCollisionAttempts {
		t.Errorf("iteration count = %d; want exactly %d", iterationCount, maxSlugCollisionAttempts)
	}
}

// ---------------------------------------------------------------------------
// TS-04-26: Verify that the hub hook inserts an orgs row with all required
//           fields correctly populated.
// Requirement: 04-REQ-7.1
// ---------------------------------------------------------------------------
func TestOrgInsert_AllRequiredFields(t *testing.T) {
	db := openOrgTestDB(t)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	err = createPersonalOrg(context.Background(), tx, "user-julia-001", "Julia", "julia@example.com")
	if err != nil {
		t.Fatalf("createPersonalOrg returned error: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Query the inserted org row.
	var id, name, slug, url, ownerID, status, createdAt, updatedAt string
	err = db.QueryRow(
		`SELECT id, name, slug, url, owner_id, status, created_at, updated_at
		 FROM orgs WHERE owner_id = ?`,
		"user-julia-001",
	).Scan(&id, &name, &slug, &url, &ownerID, &status, &createdAt, &updatedAt)
	if err != nil {
		t.Fatalf("query org by owner_id: %v", err)
	}

	// id must be a non-empty UUID.
	if id == "" {
		t.Error("org id is empty; want non-empty UUID")
	}

	// name must be the original username (not sanitized).
	if name != "Julia" {
		t.Errorf("org name = %q; want %q (original username)", name, "Julia")
	}

	// slug must be the sanitized form of the username.
	if slug != "julia" {
		t.Errorf("org slug = %q; want %q (sanitized)", slug, "julia")
	}

	// url must be empty string.
	if url != "" {
		t.Errorf("org url = %q; want empty string", url)
	}

	// owner_id must match the user ID.
	if ownerID != "user-julia-001" {
		t.Errorf("org owner_id = %q; want %q", ownerID, "user-julia-001")
	}

	// status must be 'active'.
	if status != "active" {
		t.Errorf("org status = %q; want %q", status, "active")
	}

	// created_at and updated_at must be valid RFC 3339.
	if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
		t.Errorf("org created_at %q is not valid RFC 3339: %v", createdAt, err)
	}
	if _, err := time.Parse(time.RFC3339, updatedAt); err != nil {
		t.Errorf("org updated_at %q is not valid RFC 3339: %v", updatedAt, err)
	}
}

// ---------------------------------------------------------------------------
// TS-04-27: Verify that the hub hook inserts an org_members row linking the
//           new user to the new org.
// Requirement: 04-REQ-7.2
// ---------------------------------------------------------------------------
func TestOrgInsert_OrgMembersRowCreated(t *testing.T) {
	db := openOrgTestDB(t)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	err = createPersonalOrg(context.Background(), tx, "user-karl-001", "karl", "k@test.com")
	if err != nil {
		t.Fatalf("createPersonalOrg returned error: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Get the org id.
	var orgID string
	if err := db.QueryRow("SELECT id FROM orgs WHERE owner_id = ?", "user-karl-001").Scan(&orgID); err != nil {
		t.Fatalf("query org id: %v", err)
	}

	// Verify org_members row exists and links correctly.
	var memberOrgID, memberUserID, memberCreatedAt string
	err = db.QueryRow(
		"SELECT org_id, user_id, created_at FROM org_members WHERE user_id = ?",
		"user-karl-001",
	).Scan(&memberOrgID, &memberUserID, &memberCreatedAt)
	if err != nil {
		t.Fatalf("query org_members: %v", err)
	}

	if memberOrgID != orgID {
		t.Errorf("org_members.org_id = %q; want %q (matching org)", memberOrgID, orgID)
	}
	if memberUserID != "user-karl-001" {
		t.Errorf("org_members.user_id = %q; want %q", memberUserID, "user-karl-001")
	}
	if _, err := time.Parse(time.RFC3339, memberCreatedAt); err != nil {
		t.Errorf("org_members.created_at %q is not valid RFC 3339: %v", memberCreatedAt, err)
	}
}

// ---------------------------------------------------------------------------
// TS-04-28: Verify that the hook performs all INSERT statements using the
//           *sql.Tx passed to it, not a separate connection.
// Requirement: 04-REQ-7.3
// ---------------------------------------------------------------------------
func TestOrgInsert_AllInsertsUseTx(t *testing.T) {
	db := openOrgTestDB(t)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	spy := newSpyTx(tx)

	err = doCreatePersonalOrg(context.Background(), spy, "user-100", "luna", "l@test.com")
	if err != nil {
		t.Fatalf("doCreatePersonalOrg returned error: %v", err)
	}

	// Verify exactly 2 INSERT statements were executed on the spy tx.
	if n := spy.countInserts(); n != 2 {
		t.Errorf("insert count = %d; want 2 (one for orgs, one for org_members)", n)
	}

	// Verify both INSERT INTO orgs and INSERT INTO org_members were recorded.
	if !spy.hasStatement("INSERT INTO orgs") {
		t.Error("spy did not record INSERT INTO orgs")
	}
	if !spy.hasStatement("INSERT INTO org_members") {
		t.Error("spy did not record INSERT INTO org_members")
	}
}

// ---------------------------------------------------------------------------
// TS-04-E11: Verify that if the orgs INSERT fails, the hook returns an error
//            and the transaction can be rolled back, undoing all inserts.
// Requirement: 04-REQ-7.E1
// ---------------------------------------------------------------------------
func TestOrgInsert_OrgInsertFailureRollsBack(t *testing.T) {
	db := openOrgTestDB(t)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	// Wrap tx in faultyTx that errors on INSERT INTO orgs.
	// The substring "INTO orgs (" matches the orgs insert but not org_members.
	faulty := &faultyTx{wrapped: tx, errorOnSubstr: "INTO orgs ("}

	err = doCreatePersonalOrg(context.Background(), faulty, "user-400", "failorg", "f@test.com")
	if err == nil {
		t.Fatal("doCreatePersonalOrg returned nil; want error on orgs INSERT failure")
	}

	// Rollback to free connection for verification.
	tx.Rollback()

	// Verify no org row exists for this user.
	var orgCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM orgs WHERE owner_id = ?", "user-400").Scan(&orgCount); err != nil {
		t.Fatalf("org count query: %v", err)
	}
	if orgCount != 0 {
		t.Errorf("org count = %d; want 0 after rollback", orgCount)
	}
}

// ---------------------------------------------------------------------------
// TS-04-E12: Verify that if org_members INSERT fails after orgs INSERT
//            succeeds, the hook returns an error and the transaction can be
//            rolled back, undoing all inserts.
// Requirement: 04-REQ-7.E2
// ---------------------------------------------------------------------------
func TestOrgInsert_OrgMembersInsertFailureRollsBack(t *testing.T) {
	db := openOrgTestDB(t)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	// Wrap tx in faultyTx that errors on INSERT INTO org_members only.
	faulty := &faultyTx{wrapped: tx, errorOnSubstr: "INTO org_members"}

	err = doCreatePersonalOrg(context.Background(), faulty, "user-401", "failmember", "fm@test.com")
	if err == nil {
		t.Fatal("doCreatePersonalOrg returned nil; want error on org_members INSERT failure")
	}

	// Rollback to free connection for verification.
	tx.Rollback()

	// Verify no org or org_members rows exist for this user.
	var orgCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM orgs WHERE owner_id = ?", "user-401").Scan(&orgCount); err != nil {
		t.Fatalf("org count query: %v", err)
	}
	if orgCount != 0 {
		t.Errorf("org count = %d; want 0 after rollback", orgCount)
	}

	var memberCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM org_members WHERE user_id = ?", "user-401").Scan(&memberCount); err != nil {
		t.Fatalf("member count query: %v", err)
	}
	if memberCount != 0 {
		t.Errorf("member count = %d; want 0 after rollback", memberCount)
	}
}

// ---------------------------------------------------------------------------
// TS-04-E13: Verify that if UUID generation for the new org id fails, the
//            hook returns an error immediately without performing any INSERT.
// Requirement: 04-REQ-7.E3
// ---------------------------------------------------------------------------
func TestOrgInsert_UUIDFailureNoInserts(t *testing.T) {
	db := openOrgTestDB(t)

	// Replace UUID generator with one that always fails.
	origUUID := uuidGenFn
	uuidGenFn = func() (string, error) {
		return "", fmt.Errorf("uuid gen failed")
	}
	t.Cleanup(func() { uuidGenFn = origUUID })

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	// Use spy to count INSERT statements.
	spy := newSpyTx(tx)

	err = doCreatePersonalOrg(context.Background(), spy, "user-402", "uuidfail", "u@test.com")
	if err == nil {
		t.Fatal("doCreatePersonalOrg returned nil; want error on UUID failure")
	}

	// Verify no INSERT statements were executed.
	if n := spy.countInserts(); n != 0 {
		t.Errorf("insert count = %d; want 0 (UUID failure should prevent all INSERTs)", n)
	}
}
