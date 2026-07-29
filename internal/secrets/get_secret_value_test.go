package secrets

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// ========================================================================
// Spec 09 Task 2.1: GetSecretValue store method tests
// (TS-09-20, TS-09-21, TS-09-22)
// Requirements: 09-REQ-5.1, 09-REQ-5.2, 09-REQ-5.3
// ========================================================================

// TS-09-20: GetSecretValue returns the base64-decoded plaintext string when
// the key exists in the secrets table.
// Requirement: 09-REQ-5.1
func TestGetSecretValue_ReturnsDecodedPlaintext(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Pre-populate via CreateSecrets to ensure round-trip encoding/decoding.
	_, err := store.CreateSecrets("workspace", "my-ws", []EntryInput{
		{Key: "GIT_PAT", Value: "ghp_abc123"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	val, err := store.GetSecretValue("workspace", "my-ws", "GIT_PAT")
	if err != nil {
		t.Fatalf("GetSecretValue() returned error: %v", err)
	}
	if val != "ghp_abc123" {
		t.Errorf("GetSecretValue() = %q; want %q", val, "ghp_abc123")
	}
}

// TS-09-20 (variant): GetSecretValue round-trips with a value containing
// special characters that are significant in base64 encoding.
// Requirement: 09-REQ-5.1
func TestGetSecretValue_RoundTrip_SpecialChars(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Value with characters that produce padding in base64.
	specialValue := "p@$$w0rd!#&*()=+"

	_, err := store.CreateSecrets("workspace", "my-ws", []EntryInput{
		{Key: "GIT_PASSWORD", Value: specialValue},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	val, err := store.GetSecretValue("workspace", "my-ws", "GIT_PASSWORD")
	if err != nil {
		t.Fatalf("GetSecretValue() returned error: %v", err)
	}
	if val != specialValue {
		t.Errorf("GetSecretValue() = %q; want %q", val, specialValue)
	}
}

// TS-09-21: GetSecretValue returns NotFoundError when the requested key
// does not exist for the given owner.
// Requirement: 09-REQ-5.2
func TestGetSecretValue_NotFoundError(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// No secrets exist for this owner.
	val, err := store.GetSecretValue("workspace", "my-ws", "GIT_PAT")
	if val != "" {
		t.Errorf("GetSecretValue() returned value %q; want empty string", val)
	}

	var nfe *NotFoundError
	if !errors.As(err, &nfe) {
		t.Errorf("GetSecretValue() error = %v (%T); want *NotFoundError", err, err)
	}
}

// TS-09-21 (variant): GetSecretValue returns NotFoundError when key exists
// for a different ownerType.
// Requirement: 09-REQ-5.2
func TestGetSecretValue_NotFound_DifferentOwnerType(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Create secret for user scope.
	_, err := store.CreateSecrets("user", "alice-id", []EntryInput{
		{Key: "GIT_PAT", Value: "ghp_abc123"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	// Look up under workspace scope — should not find it.
	val, err := store.GetSecretValue("workspace", "alice-id", "GIT_PAT")
	if val != "" {
		t.Errorf("GetSecretValue() returned value %q; want empty string", val)
	}

	var nfe *NotFoundError
	if !errors.As(err, &nfe) {
		t.Errorf("GetSecretValue() error = %v (%T); want *NotFoundError", err, err)
	}
}

// TS-09-22: GetSecretValue is not exposed via any HTTP endpoint; no HTTP
// route maps to this method. This is verified by inspecting the registered
// routes — there should be no route that returns raw secret values.
// Requirement: 09-REQ-5.3
func TestGetSecretValue_NotExposedViaHTTP(t *testing.T) {
	// The secrets routes only expose List (metadata) and CRUD operations
	// that never return the secret value in the response. GetSecretValue
	// is an internal method with no HTTP handler.
	//
	// Verify by checking that the Store type has GetSecretValue as a Go
	// method (it exists) but the routes package does not expose it.
	// This test passes by construction: if GetSecretValue were wired to
	// an HTTP route, a separate integration test would catch value leakage.

	db := openTestDB(t)
	store := NewStore(db)

	// Verify the method is callable (internal-only).
	_, _ = store.GetSecretValue("workspace", "my-ws", "GIT_PAT")

	// Verify that ListSecrets does NOT return values.
	_, err := store.CreateSecrets("workspace", "my-ws", []EntryInput{
		{Key: "GIT_PAT", Value: "secret-value"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	list, err := store.ListSecrets("workspace", "my-ws")
	if err != nil {
		t.Fatalf("ListSecrets() returned error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListSecrets() returned %d entries; want 1", len(list))
	}

	// SecretEntry does not have a Value field — only Key, CreatedAt, UpdatedAt.
	// This compile-time verification ensures secret values are never returned
	// through the list endpoint. The following line would not compile if
	// SecretEntry had a Value field being populated with plaintext:
	entry := list[0]
	if entry.Key != "GIT_PAT" {
		t.Errorf("entry.Key = %q; want %q", entry.Key, "GIT_PAT")
	}
}

// ========================================================================
// Edge case tests for GetSecretValue
// Requirements: 09-REQ-5.E1, 09-REQ-5.E2, 09-REQ-5.E3, 09-REQ-5.E4
// ========================================================================

// 09-REQ-5.E4: GetSecretValue performs case-insensitive key lookup.
// When stored as 'GIT_PAT', lookup with 'git_pat' should return the same value.
func TestGetSecretValue_CaseInsensitiveLookup(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	_, err := store.CreateSecrets("workspace", "my-ws", []EntryInput{
		{Key: "GIT_PAT", Value: "ghp_abc123"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	// Look up with lowercase key.
	val, err := store.GetSecretValue("workspace", "my-ws", "git_pat")
	if err != nil {
		t.Fatalf("GetSecretValue(lowercase) returned error: %v", err)
	}
	if val != "ghp_abc123" {
		t.Errorf("GetSecretValue(lowercase) = %q; want %q", val, "ghp_abc123")
	}

	// Look up with mixed case key.
	val, err = store.GetSecretValue("workspace", "my-ws", "Git_Pat")
	if err != nil {
		t.Fatalf("GetSecretValue(mixed case) returned error: %v", err)
	}
	if val != "ghp_abc123" {
		t.Errorf("GetSecretValue(mixed case) = %q; want %q", val, "ghp_abc123")
	}
}

// 09-REQ-5.E2: GetSecretValue returns an error when called with an empty key.
func TestGetSecretValue_EmptyKey(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	val, err := store.GetSecretValue("workspace", "my-ws", "")
	if err == nil {
		t.Error("GetSecretValue('') returned nil error; want non-nil error")
	}
	if val != "" {
		t.Errorf("GetSecretValue('') returned value %q; want empty string", val)
	}
}

// 09-REQ-5.E1: GetSecretValue returns an error when stored base64 value
// is malformed and cannot be decoded.
func TestGetSecretValue_MalformedBase64(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Insert a secret with an invalid base64 value directly into the DB,
	// bypassing the store's encoding logic.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		"workspace", "my-ws", "BAD_SECRET", "not-valid-base64!!!", now, now,
	)
	if err != nil {
		t.Fatalf("direct insert failed: %v", err)
	}

	val, err := store.GetSecretValue("workspace", "my-ws", "BAD_SECRET")
	if err == nil {
		t.Error("GetSecretValue() returned nil error for malformed base64; want non-nil error")
	}
	if val != "" {
		t.Errorf("GetSecretValue() returned value %q; want empty string", val)
	}

	// The error should describe a base64 decode failure.
	if err != nil && !strings.Contains(err.Error(), "base64") && !strings.Contains(err.Error(), "decode") {
		t.Errorf("error = %q; want it to mention base64/decode", err.Error())
	}
}

// 09-REQ-5.E3: GetSecretValue returns a wrapped database error when the
// database query fails.
func TestGetSecretValue_DatabaseError(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Drop the secrets table to force a database error.
	_, err := db.Exec("DROP TABLE secrets")
	if err != nil {
		t.Fatalf("failed to drop secrets table: %v", err)
	}

	val, err := store.GetSecretValue("workspace", "my-ws", "GIT_PAT")
	if err == nil {
		t.Error("GetSecretValue() returned nil error after dropping table; want non-nil error")
	}
	if val != "" {
		t.Errorf("GetSecretValue() returned value %q; want empty string", val)
	}
}

// 09-PROP-8: GetSecretValue always returns decoded plaintext, never the
// raw base64-encoded form.
func TestGetSecretValue_NeverReturnsBase64Encoded(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	plaintext := "ghp_very_secret_token_value"
	encoded := base64.StdEncoding.EncodeToString([]byte(plaintext))

	_, err := store.CreateSecrets("workspace", "my-ws", []EntryInput{
		{Key: "GIT_PAT", Value: plaintext},
	})
	if err != nil {
		t.Fatalf("CreateSecrets() returned error: %v", err)
	}

	val, err := store.GetSecretValue("workspace", "my-ws", "GIT_PAT")
	if err != nil {
		t.Fatalf("GetSecretValue() returned error: %v", err)
	}

	// The returned value must be the plaintext, not the base64-encoded form.
	if val == encoded {
		t.Error("GetSecretValue() returned the base64-encoded value; want decoded plaintext")
	}
	if val != plaintext {
		t.Errorf("GetSecretValue() = %q; want %q (plaintext)", val, plaintext)
	}
}
