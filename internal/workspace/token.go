package workspace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

// base62Alphabet contains the 62 characters used for token_id and secret generation.
const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// RandReader is the random byte source used by generateBase62.
// It defaults to crypto/rand.Reader but can be overridden in tests.
var RandReader io.Reader = rand.Reader

// generateBase62 generates a random string of length n using the base62 alphabet
// and cryptographically secure random bytes from RandReader.
//
// It reads sufficient random bytes and maps each byte to a base62 character
// using rejection sampling (discard bytes >= 62*4=248 to avoid modulo bias).
//
// Returns an error if the random source fails. The error is propagated as a Go
// error return value — no panic or os.Exit.
func generateBase62(n int) (string, error) {
	result := make([]byte, n)
	buf := make([]byte, 1)
	for i := 0; i < n; {
		_, err := io.ReadFull(RandReader, buf)
		if err != nil {
			return "", fmt.Errorf("crypto/rand read error: %w", err)
		}
		// Rejection sampling: discard values >= 248 (62*4) to avoid modulo bias.
		// For values < 248, byte % 62 gives a uniform distribution.
		if buf[0] >= 248 {
			continue
		}
		result[i] = base62Alphabet[buf[0]%62]
		i++
	}
	return string(result), nil
}

// GenerateTokenID generates an 8-character base62 token identifier.
// Returns an error if the random source fails.
func GenerateTokenID() (string, error) {
	return generateBase62(8)
}

// GenerateSecret generates a 32-character base62 secret.
// Returns an error if the random source fails.
func GenerateSecret() (string, error) {
	return generateBase62(32)
}

// HashSecret computes the SHA-256 hash of the given plaintext secret and
// returns the hex-encoded string. This is what gets stored in the
// workspace_tokens table — the plaintext is never persisted.
func HashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// AssembleToken constructs the full plaintext workspace token string in the
// format af_wt_<tokenID>_<secret>.
func AssembleToken(tokenID, secret string) string {
	return "af_wt_" + tokenID + "_" + secret
}

// allowedExpiresDays is the set of valid values for the "expires" request field.
var allowedExpiresDays = map[int]bool{
	0: true, 30: true, 60: true, 90: true,
}

// NormalizeLabel normalizes a token label:
//   - nil → nil (label omitted)
//   - pointer to empty string → nil (empty string normalized to null)
//   - pointer to non-empty string ≤128 chars → returned as-is
//   - pointer to string >128 chars → error
func NormalizeLabel(label *string) (*string, error) {
	if label == nil {
		return nil, nil
	}
	if *label == "" {
		return nil, nil
	}
	if len(*label) > 128 {
		return nil, fmt.Errorf("label must be at most 128 characters, got %d", len(*label))
	}
	return label, nil
}

// ValidateExpires validates the "expires" field from the token creation request:
//   - nil → default 30 days
//   - must be one of {0, 30, 60, 90}
//   - returns the validated integer value and an error if invalid
func ValidateExpires(expires *int) (int, error) {
	if expires == nil {
		return 30, nil
	}
	if !allowedExpiresDays[*expires] {
		return 0, fmt.Errorf("expires must be one of: 0, 30, 60, 90; got %d", *expires)
	}
	return *expires, nil
}

// ComputeExpiresAt computes the expiration timestamp given the creation time
// and the number of days until expiration.
//   - days == 0 → nil (indefinite, no expiry)
//   - days > 0 → pointer to createdAt + days formatted as ISO 8601
func ComputeExpiresAt(createdAt time.Time, days int) *string {
	if days == 0 {
		return nil
	}
	expiresAt := createdAt.AddDate(0, 0, days).UTC().Format(time.RFC3339)
	return &expiresAt
}
