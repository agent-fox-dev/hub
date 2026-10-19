package sandboxes

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

// Byte counts of the randomness behind a lease id and a lease token.
const (
	leaseIDBytes    = 16
	leaseTokenBytes = 33
)

// randomBytes reads n bytes from r. A short or failed read is an error: hub
// never makes an id or a token from less randomness than it asks for.
func randomBytes(r io.Reader, n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, fmt.Errorf("reading random bytes: %w", err)
	}
	return b, nil
}

// generateLeaseID returns "sbx_" and the lowercase hex of 16 random bytes.
func generateLeaseID(r io.Reader) (string, error) {
	b, err := randomBytes(r, leaseIDBytes)
	if err != nil {
		return "", err
	}
	return "sbx_" + hex.EncodeToString(b), nil
}

// generateToken returns a lease token: "sbl_" and base64.RawURLEncoding of 33
// random bytes, 48 characters in all.
func generateToken(r io.Reader) (string, error) {
	b, err := randomBytes(r, leaseTokenBytes)
	if err != nil {
		return "", err
	}
	return "sbl_" + base64.RawURLEncoding.EncodeToString(b), nil
}
