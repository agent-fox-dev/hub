package sandboxes

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openLeaseDB returns an empty in-memory database with one connection, the way
// the other packages' tests open theirs.
func openLeaseDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening an in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// newLeaseStore returns a store over a database holding the lease table, with
// a fixed clock.
func newLeaseStore(t *testing.T, now time.Time) (*LeaseStore, *sql.DB) {
	t.Helper()
	db := openLeaseDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema returned error: %v", err)
	}
	return NewLeaseStore(db, func() time.Time { return now }), db
}

// testToken returns a well-formed lease token that is distinct for every n.
func testToken(n int) string { return fmt.Sprintf("sbl_%044d", n) }

// rawLeaseInsert names the columns without a default; the others take theirs.
const rawLeaseInsert = `INSERT INTO sandbox_leases
	(id, workspace, profile, state, token_hash, image, network, ttl_seconds, expires_at, created_by, created_at, updated_at)
	VALUES (?, ?, 'tools', ?, ?, 'img', 'none', 3600, '2030-01-02T04:04:05Z', 'u1', '2030-01-02T03:04:05Z', '2030-01-02T03:04:05Z')`

// insertRawLease inserts a lease row straight through SQL, not through the
// store under test. hash may be nil for a NULL token hash.
func insertRawLease(t *testing.T, db *sql.DB, id, workspace, state string, hash any) {
	t.Helper()
	if _, err := db.Exec(rawLeaseInsert, id, workspace, state, hash); err != nil {
		t.Fatalf("inserting lease %s (%s): %v", id, state, err)
	}
}

// leaseID returns a well-formed lease id that is distinct for every n.
func leaseID(n int) string { return fmt.Sprintf("sbx_%032x", n) }

// TS-26-1: Lease schema initialisation is idempotent and preserves rows.
// Requirement: 26-REQ-1.1
func TestInitSchema_Idempotent_TS_26_1(t *testing.T) {
	db := openLeaseDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("first InitSchema returned error: %v", err)
	}
	insertRawLease(t, db, leaseID(1), "widgets", "leased", testToken(1))
	var before string
	if err := db.QueryRow(`SELECT id || workspace || state || token_hash || updated_at FROM sandbox_leases`).Scan(&before); err != nil {
		t.Fatalf("reading the row: %v", err)
	}

	if err := InitSchema(db); err != nil {
		t.Fatalf("second InitSchema returned error: %v", err)
	}
	var after string
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*), MAX(id || workspace || state || token_hash || updated_at) FROM sandbox_leases`).Scan(&rows, &after); err != nil {
		t.Fatalf("reading the row again: %v", err)
	}
	if rows != 1 || after != before {
		t.Errorf("after the second InitSchema: %d rows, %q; want the one unchanged row %q", rows, after, before)
	}
}

// TS-26-2: The state CHECK accepts exactly the five states, and the token hash
// is unique and nullable.
// Requirement: 26-REQ-1.2
func TestInitSchema_StateAndTokenHashConstraints_TS_26_2(t *testing.T) {
	_, db := newLeaseStore(t, time.Now())

	for i, state := range []string{"leased", "active", "destroyed", "expired", "failed"} {
		if _, err := db.Exec(rawLeaseInsert, leaseID(i), "widgets", state, testToken(i)); err != nil {
			t.Errorf("state %q was refused: %v", state, err)
		}
	}
	if _, err := db.Exec(rawLeaseInsert, leaseID(100), "widgets", "bogus", testToken(100)); err == nil {
		t.Error("state 'bogus' was accepted")
	}

	for i := range 2 {
		if _, err := db.Exec(rawLeaseInsert, leaseID(200+i), "widgets", "destroyed", nil); err != nil {
			t.Errorf("NULL token hash number %d was refused: %v", i+1, err)
		}
	}
	if _, err := db.Exec(rawLeaseInsert, leaseID(300), "widgets", "leased", "same-hash"); err != nil {
		t.Fatalf("first use of a hash was refused: %v", err)
	}
	if _, err := db.Exec(rawLeaseInsert, leaseID(301), "widgets", "leased", "same-hash"); err == nil {
		t.Error("a second row with the same non-NULL hash was accepted")
	}
}

// A lease written through the store reads back with every field, including the
// JSON columns, and a missing id is ErrLeaseNotFound.
// Requirement: 26-REQ-1.4
func TestLeaseStore_InsertAndGetRoundTrip(t *testing.T) {
	store, _ := newLeaseStore(t, time.Now())
	want := &Lease{
		ID: leaseID(7), Workspace: "widgets", Profile: "tools", Purpose: "triage", State: "leased",
		Image: "registry.example/tools:1", Network: "none",
		Resources:       Resources{CPUs: 1.5, MemoryMB: 512, PIDs: 100, DiskMB: 0},
		RequireLandlock: true,
		Secrets:         []string{"GIT_PAT"}, OptionalSecrets: []string{"EXTRA"},
		Env:        map[string]string{"LANG": "C"},
		Labels:     map[string]string{"k": "v"},
		TTLSeconds: 3600, ExpiresAt: "2030-01-02T04:04:05Z", CreatedBy: "u1",
		CreatedAt: "2030-01-02T03:04:05Z", UpdatedAt: "2030-01-02T03:04:05Z",
	}
	if err := store.InsertLease(store.db, want, HashToken(testToken(7))); err != nil {
		t.Fatalf("InsertLease returned error: %v", err)
	}

	got, err := store.GetLease(want.ID)
	if err != nil {
		t.Fatalf("GetLease returned error: %v", err)
	}
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
		t.Errorf("read back %+v\nwant      %+v", got, want)
	}
	if got.SandboxID != nil || got.Error != nil || got.EndedAt != nil || got.EndReason != nil {
		t.Errorf("sandbox_id, error, ended_at or end_reason is set on a new lease: %+v", got)
	}

	if _, err := store.GetLease(leaseID(8)); !errors.Is(err, ErrLeaseNotFound) {
		t.Errorf("GetLease(unknown) error = %v; want ErrLeaseNotFound", err)
	}
}

// TS-26-36: Only the lowercase-hex SHA-256 of the token is stored.
// Requirement: 26-REQ-6.2
func TestHashToken_TS_26_36(t *testing.T) {
	token := testToken(36)
	sum := sha256.Sum256([]byte(token))
	want := hex.EncodeToString(sum[:])
	if got := HashToken(token); got != want {
		t.Fatalf("HashToken = %q; want %q", got, want)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(want) {
		t.Fatalf("test setup: %q is not 64 lowercase hex characters", want)
	}

	store, db := newLeaseStore(t, time.Now())
	l := &Lease{ID: leaseID(36), Workspace: "widgets", Profile: "tools", State: "leased", Image: "img", Network: "none",
		TTLSeconds: 60, ExpiresAt: "2030-01-02T04:04:05Z", CreatedBy: "u1", CreatedAt: "2030-01-02T03:04:05Z", UpdatedAt: "2030-01-02T03:04:05Z"}
	if err := store.InsertLease(db, l, HashToken(token)); err != nil {
		t.Fatalf("InsertLease returned error: %v", err)
	}
	var stored string
	if err := db.QueryRow(`SELECT token_hash FROM sandbox_leases WHERE id = ?`, l.ID).Scan(&stored); err != nil {
		t.Fatalf("reading the hash: %v", err)
	}
	if stored != want {
		t.Errorf("stored hash = %q; want %q", stored, want)
	}
}

// TS-26-38: Token lookup returns the lease for a live token.
// Requirement: 26-REQ-6.4
func TestLookupByToken_LiveLeases_TS_26_38(t *testing.T) {
	store, db := newLeaseStore(t, time.Now())
	insertRawLease(t, db, leaseID(1), "widgets", "leased", HashToken(testToken(1)))
	insertRawLease(t, db, leaseID(2), "gadgets", "active", HashToken(testToken(2)))

	for n, wantState := range map[int]string{1: "leased", 2: "active"} {
		got, err := store.LookupByToken(testToken(n))
		if err != nil {
			t.Fatalf("LookupByToken(token %d) returned error: %v", n, err)
		}
		if got.ID != leaseID(n) || got.State != wantState {
			t.Errorf("LookupByToken(token %d) = %s in %s; want %s in %s", n, got.ID, got.State, leaseID(n), wantState)
		}
	}
}

// TS-26-39: Token lookup returns the same not-found error for every unusable
// token.
// Requirement: 26-REQ-6.5
func TestLookupByToken_NotFound_TS_26_39(t *testing.T) {
	store, db := newLeaseStore(t, time.Now())
	insertRawLease(t, db, leaseID(1), "widgets", "leased", HashToken(testToken(1)))
	// Terminal leases that kept their hash, which revocation would have cleared:
	// the state filter alone must refuse them.
	insertRawLease(t, db, leaseID(2), "widgets", "destroyed", HashToken(testToken(2)))
	insertRawLease(t, db, leaseID(3), "widgets", "expired", HashToken(testToken(3)))
	insertRawLease(t, db, leaseID(4), "widgets", "failed", HashToken(testToken(4)))

	cases := map[string]string{
		"empty":               "",
		"too short":           "abc",
		"no prefix":           "xyz_" + strings.Repeat("a", 44),
		"47 characters":       testToken(1)[:47],
		"49 characters":       testToken(1) + "0",
		"illegal character":   "sbl_" + strings.Repeat("a", 43) + "!",
		"unknown well-formed": testToken(99),
		"destroyed":           testToken(2),
		"expired":             testToken(3),
		"failed":              testToken(4),
	}
	for name, token := range cases {
		got, err := store.LookupByToken(token)
		if got != nil || err != ErrLeaseNotFound {
			t.Errorf("%s: LookupByToken = (%v, %v); want (nil, ErrLeaseNotFound)", name, got, err)
		}
	}
}

// TS-26-40: Revocation ends the lease and clears the hash in one update.
// Requirement: 26-REQ-6.6
func TestRevokeToken_TS_26_40(t *testing.T) {
	store, db := newLeaseStore(t, time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	insertRawLease(t, db, leaseID(1), "widgets", "leased", HashToken(testToken(1)))
	if _, err := store.LookupByToken(testToken(1)); err != nil {
		t.Fatalf("the token does not work before revocation: %v", err)
	}

	if err := store.RevokeToken(leaseID(1)); err != nil {
		t.Fatalf("RevokeToken returned error: %v", err)
	}
	var state string
	var hash sql.NullString
	if err := db.QueryRow(`SELECT state, token_hash FROM sandbox_leases WHERE id = ?`, leaseID(1)).Scan(&state, &hash); err != nil {
		t.Fatalf("reading the row: %v", err)
	}
	if hash.Valid {
		t.Errorf("token hash after revocation = %q; want NULL", hash.String)
	}
	if state != "destroyed" && state != "expired" && state != "failed" {
		t.Errorf("state after revocation = %q; want a terminal state", state)
	}
	for range 2 {
		if got, err := store.LookupByToken(testToken(1)); got != nil || err != ErrLeaseNotFound {
			t.Errorf("LookupByToken after revocation = (%v, %v); want (nil, ErrLeaseNotFound)", got, err)
		}
	}
}

// TS-26-52: All lease endings go through one function that conditionally
// updates live leases.
// Requirement: 26-REQ-8.4
func TestEndLease_TS_26_52(t *testing.T) {
	end := time.Date(2030, 1, 2, 5, 0, 0, 0, time.UTC)
	store, db := newLeaseStore(t, end)
	insertRawLease(t, db, leaseID(1), "widgets", "leased", HashToken(testToken(1)))
	insertRawLease(t, db, leaseID(2), "widgets", "active", HashToken(testToken(2)))
	insertRawLease(t, db, leaseID(3), "widgets", "destroyed", nil)

	read := func(id string) (state, endedAt, updatedAt, reason string, hash sql.NullString) {
		var ended, why sql.NullString
		if err := db.QueryRow(`SELECT state, ended_at, updated_at, end_reason, token_hash FROM sandbox_leases WHERE id = ?`, id).
			Scan(&state, &ended, &updatedAt, &why, &hash); err != nil {
			t.Fatalf("reading %s: %v", id, err)
		}
		return state, ended.String, updatedAt, why.String, hash
	}

	for _, id := range []string{leaseID(1), leaseID(2)} {
		changed, err := store.EndLease(&Lease{ID: id}, "destroyed", "api")
		if err != nil || !changed {
			t.Fatalf("EndLease(%s) = (%v, %v); want (true, nil)", id, changed, err)
		}
		state, endedAt, updatedAt, reason, hash := read(id)
		if state != "destroyed" || reason != "api" || endedAt != "2030-01-02T05:00:00Z" || updatedAt != endedAt || hash.Valid {
			t.Errorf("%s after EndLease: state %q, reason %q, ended_at %q, updated_at %q, hash %v; want destroyed/api/05:00:00Z, hash NULL",
				id, state, reason, endedAt, updatedAt, hash)
		}
	}

	before1, before2, before3, before4, _ := read(leaseID(3))
	changed, err := store.EndLease(&Lease{ID: leaseID(3)}, "destroyed", "api")
	if err != nil || changed {
		t.Errorf("EndLease on an ended lease = (%v, %v); want (false, nil)", changed, err)
	}
	if a1, a2, a3, a4, _ := read(leaseID(3)); a1 != before1 || a2 != before2 || a3 != before3 || a4 != before4 {
		t.Errorf("an ended lease changed: %q %q %q %q -> %q %q %q %q", before1, before2, before3, before4, a1, a2, a3, a4)
	}

	insertRawLease(t, db, leaseID(4), "widgets", "leased", HashToken(testToken(4)))
	for _, target := range []string{"leased", "active", "bogus", ""} {
		if changed, err := store.EndLease(&Lease{ID: leaseID(4)}, target, "api"); err == nil || changed {
			t.Errorf("EndLease to %q = (%v, %v); want an error and no change", target, changed, err)
		}
	}
	if state, _, _, _, _ := read(leaseID(4)); state != "leased" {
		t.Errorf("a refused EndLease changed the lease to %q", state)
	}
	for _, target := range []string{"expired", "failed"} {
		insertRawLease(t, db, leaseID(10+len(target)), "widgets", "active", HashToken(testToken(10+len(target))))
		if changed, err := store.EndLease(&Lease{ID: leaseID(10 + len(target))}, target, "timeout"); err != nil || !changed {
			t.Errorf("EndLease to %q = (%v, %v); want (true, nil)", target, changed, err)
		}
	}
}

// No production file ends a lease anywhere but in EndLease: clearing the token
// hash is the mark of an ending, and exactly one statement does it.
// Requirement: 26-REQ-8.4
func TestOnlyEndLeaseClearsTheTokenHash(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(src)), "token_hash = null") {
			found = append(found, f)
		}
	}
	if len(found) != 1 || found[0] != "lease.go" {
		t.Errorf("files that clear a token hash = %v; want only lease.go (EndLease)", found)
	}
}
