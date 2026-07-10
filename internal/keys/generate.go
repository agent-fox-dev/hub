package keys

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// base62Charset is the set of characters used for base62 encoding.
// Characters [0-9A-Za-z] produce alphanumeric-only output.
const base62Charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// base62CharsetLen is the length of the base62 charset (62).
var base62CharsetLen = big.NewInt(int64(len(base62Charset)))

// generateBase62 generates a cryptographically secure random string of the
// given length using base62 encoding. It uses crypto/rand as the sole entropy
// source with rejection sampling (via crypto/rand.Int) to avoid modulo bias.
func generateBase62(length int) (string, error) {
	result := make([]byte, length)
	for i := range result {
		idx, err := rand.Int(rand.Reader, base62CharsetLen)
		if err != nil {
			return "", fmt.Errorf("crypto/rand failed: %w", err)
		}
		result[i] = base62Charset[idx.Int64()]
	}
	return string(result), nil
}

// GenerateKeyID generates an 8-character base62-encoded alphanumeric key
// identifier using crypto/rand as the entropy source.
func GenerateKeyID() (string, error) {
	return generateBase62(8)
}

// GenerateSecret generates a 32-character base62-encoded alphanumeric secret
// using crypto/rand as the entropy source.
func GenerateSecret() (string, error) {
	return generateBase62(32)
}

// BuildToken assembles the full API key token in the format af_<key_id>_<secret>.
func BuildToken(keyID, secret string) string {
	return "af_" + keyID + "_" + secret
}
