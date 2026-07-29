package secrets

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-07-6: Verifies that the API accepts raw values from 0 bytes (empty string)
// up to 262144 bytes, encodes them for storage, and returns decoded values to
// clients.
// Requirement: 07-REQ-3.1
// ---------------------------------------------------------------------------

func TestStoreAcceptsEmptyValue(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	entries, err := store.CreateVariables("user", "test-user", []EntryInput{
		{Key: "EMPTY_VAR", Value: ""},
	})
	if err != nil {
		t.Fatalf("CreateVariables() with empty value returned error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries; want 1", len(entries))
	}
	if entries[0].Value != "" {
		t.Errorf("value = %q; want empty string", entries[0].Value)
	}
}

func TestStoreAcceptsMaxSizeValue(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	largeValue := strings.Repeat("A", MaxValueSize) // exactly 262144 bytes
	entries, err := store.CreateVariables("user", "test-user", []EntryInput{
		{Key: "LARGE_VAR", Value: largeValue},
	})
	if err != nil {
		t.Fatalf("CreateVariables() with %d-byte value returned error: %v", MaxValueSize, err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries; want 1", len(entries))
	}
	if entries[0].Value != largeValue {
		t.Errorf("value length = %d; want %d", len(entries[0].Value), MaxValueSize)
	}
}

// 07-REQ-3.E4: Multi-line content (PEM certificate, YAML) round-trips correctly.
func TestStoreBase64RoundTrip_MultilineContent(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	pemValue := "-----BEGIN CERTIFICATE-----\nMIIBkTCB+wIJAL...\n-----END CERTIFICATE-----\n"
	entries, err := store.CreateVariables("user", "test-user", []EntryInput{
		{Key: "CERT", Value: pemValue},
	})
	if err != nil {
		t.Fatalf("CreateVariables() with multi-line value returned error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries; want 1", len(entries))
	}
	if entries[0].Value != pemValue {
		t.Errorf("round-trip value mismatch;\ngot:  %q\nwant: %q", entries[0].Value, pemValue)
	}
}

// 07-REQ-3.E4: YAML content with special characters round-trips correctly.
func TestStoreBase64RoundTrip_YAMLContent(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	yamlValue := "config:\n  key: \"value\"\n  nested:\n    - item1\n    - item2\n"
	entries, err := store.CreateVariables("user", "test-user", []EntryInput{
		{Key: "YAML_CONFIG", Value: yamlValue},
	})
	if err != nil {
		t.Fatalf("CreateVariables() with YAML value returned error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries; want 1", len(entries))
	}
	if entries[0].Value != yamlValue {
		t.Errorf("round-trip value mismatch;\ngot:  %q\nwant: %q", entries[0].Value, yamlValue)
	}
}

// ---------------------------------------------------------------------------
// TS-07-7: Verifies that a raw value exceeding 262144 bytes is rejected with
// an error before any entries are written.
// Requirement: 07-REQ-3.2
// ---------------------------------------------------------------------------

func TestValidateValue_ExceedsMaxSize(t *testing.T) {
	oversized := strings.Repeat("A", MaxValueSize+1) // 262145 bytes
	if err := ValidateValue(oversized); err == nil {
		t.Error("ValidateValue(262145 bytes) returned nil; want error for exceeding max size")
	}
}

// 07-REQ-3.E3: Value exactly at the 256 KB limit is accepted.
func TestValidateValue_ExactlyMaxSize(t *testing.T) {
	maxVal := strings.Repeat("A", MaxValueSize) // 262144 bytes
	if err := ValidateValue(maxVal); err != nil {
		t.Errorf("ValidateValue(262144 bytes) returned error: %v; want nil", err)
	}
}

func TestValidateValue_EmptyString(t *testing.T) {
	if err := ValidateValue(""); err != nil {
		t.Errorf("ValidateValue(\"\") returned error: %v; want nil (empty is valid)", err)
	}
}

// Store-level test: creation with an oversized value is rejected and no entries
// are written.
func TestStoreRejectsOversizedValue(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	oversized := strings.Repeat("A", MaxValueSize+1)
	_, err := store.CreateVariables("user", "test-user", []EntryInput{
		{Key: "TOO_BIG", Value: oversized},
	})
	if err == nil {
		t.Fatal("CreateVariables() with oversized value returned nil; want error")
	}

	// Verify no entries were written.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM variables WHERE owner_type = ? AND owner_id = ?",
		"user", "test-user").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d; want 0 (no writes on validation failure)", count)
	}
}
