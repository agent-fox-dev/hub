package secrets

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-07-10: Verifies that a POST request with duplicate keys (case-insensitively)
// in the entries array is rejected before any writes.
// Requirement: 07-REQ-5.1
// ---------------------------------------------------------------------------

func TestValidateEntries_DuplicateExactMatch(t *testing.T) {
	err := ValidateEntries([]EntryInput{
		{Key: "DB_HOST", Value: "v1"},
		{Key: "DB_HOST", Value: "v2"},
	})
	if err == nil {
		t.Error("ValidateEntries() with exact duplicate keys returned nil; want error")
	}
}

// 07-REQ-5.E1: Keys that differ only by case are treated as duplicates.
func TestValidateEntries_DuplicateCaseInsensitive(t *testing.T) {
	err := ValidateEntries([]EntryInput{
		{Key: "DB_HOST", Value: "v1"},
		{Key: "db_host", Value: "v2"},
	})
	if err == nil {
		t.Error("ValidateEntries() with case-insensitive duplicate keys returned nil; want error")
	}
}

// Verify the duplicate error message identifies the conflicting key.
func TestValidateEntries_DuplicateErrorMessage(t *testing.T) {
	err := ValidateEntries([]EntryInput{
		{Key: "DB_HOST", Value: "v1"},
		{Key: "db_host", Value: "v2"},
	})
	if err == nil {
		t.Fatal("ValidateEntries() with duplicate keys returned nil; want error")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "duplicate") {
		t.Errorf("error = %q; want message containing 'duplicate'", err.Error())
	}
}

// Store-level test: no entries are written when intra-request duplicates are detected.
func TestStoreDuplicateKey_NoWrites(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	_, err := store.CreateSecrets("user", "test-user", []EntryInput{
		{Key: "DB_HOST", Value: "v1"},
		{Key: "db_host", Value: "v2"},
	})
	if err == nil {
		t.Fatal("CreateSecrets() with duplicate keys returned nil; want error")
	}

	// No entries should have been written.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		"user", "test-user").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d; want 0 (no writes on duplicate detection)", count)
	}
}

// ---------------------------------------------------------------------------
// TS-07-11: Verifies that a POST request with an empty entries array is
// rejected before any database writes.
// Requirement: 07-REQ-5.2
// ---------------------------------------------------------------------------

func TestValidateEntries_EmptyArray(t *testing.T) {
	err := ValidateEntries([]EntryInput{})
	if err == nil {
		t.Error("ValidateEntries(empty) returned nil; want error for empty entries array")
	}
}

// Store-level test: empty entries rejected for secrets.
func TestStoreRejectsEmptyEntries_Secrets(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	_, err := store.CreateSecrets("user", "test-user", []EntryInput{})
	if err == nil {
		t.Fatal("CreateSecrets(empty entries) returned nil; want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "empty") && !strings.Contains(msg, "entries") {
		t.Errorf("error = %q; want message about empty entries", msg)
	}
}

// Store-level test: empty entries rejected for variables.
func TestStoreRejectsEmptyEntries_Variables(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	_, err := store.CreateVariables("user", "test-user", []EntryInput{})
	if err == nil {
		t.Fatal("CreateVariables(empty entries) returned nil; want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "empty") && !strings.Contains(msg, "entries") {
		t.Errorf("error = %q; want message about empty entries", msg)
	}
}

// 07-REQ-5.E2: Single entry with a valid key passes duplicate detection.
func TestValidateEntries_SingleEntryValid(t *testing.T) {
	err := ValidateEntries([]EntryInput{
		{Key: "SINGLE_KEY", Value: "value"},
	})
	if err != nil {
		t.Errorf("ValidateEntries(single entry) returned error: %v; want nil", err)
	}
}

// Verify that ValidateEntries rejects nil/zero-length slices consistently.
func TestValidateEntries_NilSlice(t *testing.T) {
	err := ValidateEntries(nil)
	if err == nil {
		t.Error("ValidateEntries(nil) returned nil; want error for empty entries")
	}
}
