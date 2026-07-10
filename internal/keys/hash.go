package keys

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// HashSecret returns the lowercase hex-encoded SHA-256 hash of the given
// plaintext secret. The plaintext secret must never be stored persistently;
// only this hash value is written to the api_keys table.
func HashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// ComputeExpiresAt calculates the key expiration timestamp.
// When expiresInDays is 0, the key is indefinite and nil is returned.
// Otherwise, it returns a pointer to from + (24h * expiresInDays).
func ComputeExpiresAt(expiresInDays int, from time.Time) *time.Time {
	if expiresInDays == 0 {
		return nil
	}
	t := from.Add(time.Duration(expiresInDays) * 24 * time.Hour)
	return &t
}
