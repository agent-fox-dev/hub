package secrets

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-07-8: Verifies that a POST request that would push a scope past 100
// entries is rejected with the specific message about the limit.
// Requirement: 07-REQ-4.1
// ---------------------------------------------------------------------------

func TestScopeLimit_AcceptsUnderLimit(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Seed 50 secrets.
	seedSecrets(t, db, "user", "test-user", 50)

	// Adding 1 more should succeed (51 total, well under 100).
	_, err := store.CreateSecrets("user", "test-user", []EntryInput{
		{Key: "KEY_EXTRA", Value: "v"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() under limit returned error: %v", err)
	}
}

// 07-REQ-4.E1: POST bringing scope to exactly 100 entries is accepted.
func TestScopeLimit_AcceptsAtExactlyLimit(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Seed 99 secrets.
	seedSecrets(t, db, "user", "test-user", 99)

	// Adding 1 more brings total to exactly 100 — should succeed.
	_, err := store.CreateSecrets("user", "test-user", []EntryInput{
		{Key: "KEY_100", Value: "v"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() at exact limit returned error: %v", err)
	}

	// Verify count is exactly 100.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"user", "test-user").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != MaxEntriesPerScope {
		t.Errorf("count = %d; want %d", count, MaxEntriesPerScope)
	}
}

// 07-REQ-4.E2: POST bringing scope to 101 entries (one over the limit) is rejected.
func TestScopeLimit_RejectsOneOverLimit(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Seed 99 secrets.
	seedSecrets(t, db, "user", "test-user", 99)

	// Adding 2 more would push to 101 — must be rejected.
	_, err := store.CreateSecrets("user", "test-user", []EntryInput{
		{Key: "KEY_100", Value: "v1"},
		{Key: "KEY_101", Value: "v2"},
	})
	if err == nil {
		t.Fatal("CreateSecrets() exceeding limit returned nil; want error")
	}

	// Error message must contain the expected text.
	want := "maximum of 100 entries per scope exceeded"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q; want message containing %q", err.Error(), want)
	}

	// No entries should have been written — count remains 99.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"user", "test-user").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 99 {
		t.Errorf("count = %d; want 99 (no writes)", count)
	}
}

// 07-REQ-4.E3: All-or-nothing — no partial writes when the limit is exceeded
// by a batch that collectively pushes the scope over 100.
func TestScopeLimit_AllOrNothing(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Seed 98 secrets.
	seedSecrets(t, db, "user", "test-user", 98)

	// Adding 3 more would push to 101 — entire request must be rejected.
	_, err := store.CreateSecrets("user", "test-user", []EntryInput{
		{Key: "KEY_99", Value: "v1"},
		{Key: "KEY_100", Value: "v2"},
		{Key: "KEY_101", Value: "v3"},
	})
	if err == nil {
		t.Fatal("CreateSecrets() exceeding limit returned nil; want error")
	}

	// Count should remain 98 — no partial writes.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"user", "test-user").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 98 {
		t.Errorf("count = %d; want 98 (no partial writes)", count)
	}
}

// Test that the limit is enforced per (owner_type, owner_id) independently.
// A full scope for one owner does not affect a different owner.
func TestScopeLimit_IndependentPerScope(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Seed 99 secrets for user-a.
	seedSecrets(t, db, "user", "user-a", 99)

	// Adding 1 secret for a different user should succeed — independent scope.
	_, err := store.CreateSecrets("user", "user-b", []EntryInput{
		{Key: "KEY_0", Value: "v"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() for different scope returned error: %v", err)
	}
}

// Test that the limit applies separately per owner_type even for the same ID.
func TestScopeLimit_IndependentPerOwnerType(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Seed 99 secrets for owner_type=user, owner_id=shared-id.
	seedSecrets(t, db, "user", "shared-id", 99)

	// Adding 1 secret for owner_type=org, owner_id=shared-id should succeed.
	_, err := store.CreateSecrets("org", "shared-id", []EntryInput{
		{Key: "ORG_KEY", Value: "v"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() for different owner_type returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TS-07-9: Verifies that the count check and INSERT are performed within the
// same write transaction to ensure atomicity under concurrent access.
// Requirement: 07-REQ-4.2
// ---------------------------------------------------------------------------

func TestScopeLimit_TransactionalAtomicity(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Seed 99 secrets.
	seedSecrets(t, db, "user", "test-user", 99)

	// Concurrently attempt to add one secret each from two goroutines.
	// At most one should succeed; the scope must never exceed 100.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := store.CreateSecrets("user", "test-user", []EntryInput{
				{Key: fmt.Sprintf("CONCURRENT_%d", idx), Value: "v"},
			})
			results[idx] = err
		}(i)
	}
	wg.Wait()

	// At most one of the concurrent requests should succeed.
	successCount := 0
	for _, err := range results {
		if err == nil {
			successCount++
		}
	}
	if successCount > 1 {
		t.Errorf("got %d successful concurrent creates; want at most 1", successCount)
	}

	// Final count must not exceed MaxEntriesPerScope.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"user", "test-user").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count > MaxEntriesPerScope {
		t.Errorf("final count = %d; want <= %d", count, MaxEntriesPerScope)
	}
}
