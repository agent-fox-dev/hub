package secrets

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// ValidateKey checks that a key name follows the naming rules:
//   - Contains only alphanumeric characters and underscores
//   - Does not start with a digit
//   - Is at most MaxKeyLength (255) characters long
//   - Is not empty
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("key must not be empty")
	}
	if len(key) > MaxKeyLength {
		return fmt.Errorf("key %q exceeds maximum length of %d characters", key, MaxKeyLength)
	}
	// Must not start with a digit.
	if key[0] >= '0' && key[0] <= '9' {
		return fmt.Errorf("key %q must not start with a digit", key)
	}
	// Must contain only alphanumeric characters and underscores.
	for _, c := range key {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return fmt.Errorf("key %q contains invalid character %q; only alphanumeric and underscore allowed", key, string(c))
		}
	}
	return nil
}

// ValidateValue checks that a raw value does not exceed MaxValueSize (262144 bytes).
func ValidateValue(value string) error {
	if len(value) > MaxValueSize {
		return fmt.Errorf("value size %d bytes exceeds maximum of %d bytes (256 KB)", len(value), MaxValueSize)
	}
	return nil
}

// ValidateEntries checks the entries array:
//   - Must not be empty
//   - Must not contain duplicate keys (case-insensitive)
//   - Each key and value must pass individual validation
func ValidateEntries(entries []EntryInput) error {
	if len(entries) == 0 {
		return fmt.Errorf("entries array must not be empty")
	}

	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if err := ValidateKey(e.Key); err != nil {
			return err
		}
		if err := ValidateValue(e.Value); err != nil {
			return err
		}
		upper := strings.ToUpper(e.Key)
		if seen[upper] {
			return fmt.Errorf("duplicate key %q in entries (case-insensitive)", e.Key)
		}
		seen[upper] = true
	}
	return nil
}

// EncodeValue encodes a raw value as a base64 string for storage.
func EncodeValue(raw string) string {
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// DecodeValue decodes a base64-encoded value from storage.
func DecodeValue(encoded string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64 value: %w", err)
	}
	return string(b), nil
}
