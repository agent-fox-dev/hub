package secrets

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-07-4: Verifies that keys containing only alphanumeric characters and
// underscores, not starting with a digit, and at most 255 characters are
// accepted as valid.
// Requirement: 07-REQ-2.1
// ---------------------------------------------------------------------------

func TestValidateKey_ValidAlphanumericUnderscore(t *testing.T) {
	if err := ValidateKey("VALID_KEY_123"); err != nil {
		t.Errorf("ValidateKey(%q) returned error: %v; want nil", "VALID_KEY_123", err)
	}
}

func TestValidateKey_ValidSingleCharacter(t *testing.T) {
	if err := ValidateKey("A"); err != nil {
		t.Errorf("ValidateKey(%q) returned error: %v; want nil", "A", err)
	}
}

func TestValidateKey_ValidAllLowercase(t *testing.T) {
	if err := ValidateKey("my_key"); err != nil {
		t.Errorf("ValidateKey(%q) returned error: %v; want nil", "my_key", err)
	}
}

func TestValidateKey_ValidUnderscorePrefix(t *testing.T) {
	if err := ValidateKey("_private"); err != nil {
		t.Errorf("ValidateKey(%q) returned error: %v; want nil", "_private", err)
	}
}

// 07-REQ-2.E2: Key exactly 255 characters long — at the limit, accepted.
func TestValidateKey_ExactlyMaxLength(t *testing.T) {
	key := strings.Repeat("A", MaxKeyLength) // 255 chars
	if err := ValidateKey(key); err != nil {
		t.Errorf("ValidateKey(255 chars) returned error: %v; want nil", err)
	}
}

// ---------------------------------------------------------------------------
// TS-07-5: Verifies that a key containing invalid characters (e.g., hyphen)
// is rejected before any entries are written.
// Requirement: 07-REQ-2.2
// ---------------------------------------------------------------------------

func TestValidateKey_InvalidHyphen(t *testing.T) {
	if err := ValidateKey("INVALID-KEY"); err == nil {
		t.Error("ValidateKey(\"INVALID-KEY\") returned nil; want error for invalid character '-'")
	}
}

func TestValidateKey_InvalidDot(t *testing.T) {
	if err := ValidateKey("INVALID.KEY"); err == nil {
		t.Error("ValidateKey(\"INVALID.KEY\") returned nil; want error for invalid character '.'")
	}
}

func TestValidateKey_InvalidSpace(t *testing.T) {
	if err := ValidateKey("INVALID KEY"); err == nil {
		t.Error("ValidateKey(\"INVALID KEY\") returned nil; want error for space character")
	}
}

func TestValidateKey_InvalidSpecialChars(t *testing.T) {
	for _, key := range []string{"KEY!", "KEY@HOST", "KEY#1", "KEY$VAR", "KEY%25"} {
		if err := ValidateKey(key); err == nil {
			t.Errorf("ValidateKey(%q) returned nil; want error for special character", key)
		}
	}
}

// 07-REQ-2.E4: Key starts with a digit — rejected.
func TestValidateKey_StartsWithDigit(t *testing.T) {
	if err := ValidateKey("1_KEY"); err == nil {
		t.Error("ValidateKey(\"1_KEY\") returned nil; want error for key starting with digit")
	}
}

func TestValidateKey_StartsWithDigitOnly(t *testing.T) {
	if err := ValidateKey("123"); err == nil {
		t.Error("ValidateKey(\"123\") returned nil; want error for key starting with digit")
	}
}

// 07-REQ-2.E1: Key exactly 256 characters long — one over the limit, rejected.
func TestValidateKey_ExceedsMaxLength(t *testing.T) {
	key := strings.Repeat("A", MaxKeyLength+1) // 256 chars
	if err := ValidateKey(key); err == nil {
		t.Error("ValidateKey(256 chars) returned nil; want error for exceeding max length")
	}
}

// 07-REQ-2.E3: Empty key — rejected.
func TestValidateKey_EmptyString(t *testing.T) {
	if err := ValidateKey(""); err == nil {
		t.Error("ValidateKey(\"\") returned nil; want error for empty key")
	}
}

// ---------------------------------------------------------------------------
// 07-REQ-2.E5: Cross-request duplicate key — store rejects a key that already
// exists at the same (owner_type, owner_id) scope.
// ---------------------------------------------------------------------------

func TestStoreCrossRequestDuplicateKey(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// First create should succeed.
	_, err := store.CreateSecrets("user", "test-user", []EntryInput{
		{Key: "MY_KEY", Value: "value1"},
	})
	if err != nil {
		t.Fatalf("first CreateSecrets() returned error: %v", err)
	}

	// Second create with the same key should fail with a conflict error.
	_, err = store.CreateSecrets("user", "test-user", []EntryInput{
		{Key: "MY_KEY", Value: "value2"},
	})
	if err == nil {
		t.Error("second CreateSecrets() with duplicate key returned nil; want conflict error")
	}
}

// 07-REQ-2.1: Case-insensitive uniqueness across requests within same scope.
func TestStoreCrossRequestDuplicateKey_CaseInsensitive(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	// Create with "DB_HOST".
	_, err := store.CreateSecrets("user", "test-user", []EntryInput{
		{Key: "DB_HOST", Value: "value1"},
	})
	if err != nil {
		t.Fatalf("CreateSecrets(DB_HOST) returned error: %v", err)
	}

	// Create with "db_host" (different case) should fail — case-insensitive conflict.
	_, err = store.CreateSecrets("user", "test-user", []EntryInput{
		{Key: "db_host", Value: "value2"},
	})
	if err == nil {
		t.Error("CreateSecrets(db_host) after DB_HOST returned nil; want case-insensitive conflict error")
	}
}
