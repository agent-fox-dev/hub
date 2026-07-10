package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// generateBase62 tests
// ---------------------------------------------------------------------------

func TestGenerateBase62_Length(t *testing.T) {
	for _, n := range []int{1, 8, 32, 64} {
		s, err := generateBase62(n)
		if err != nil {
			t.Fatalf("generateBase62(%d) error: %v", n, err)
		}
		if len(s) != n {
			t.Errorf("generateBase62(%d) length = %d, want %d", n, len(s), n)
		}
	}
}

func TestGenerateBase62_OnlyBase62Chars(t *testing.T) {
	base62Re := regexp.MustCompile(`^[0-9A-Za-z]+$`)
	s, err := generateBase62(100)
	if err != nil {
		t.Fatalf("generateBase62(100) error: %v", err)
	}
	if !base62Re.MatchString(s) {
		t.Errorf("generateBase62(100) = %q; contains non-base62 characters", s)
	}
}

func TestGenerateBase62_ErrorPropagation(t *testing.T) {
	// TS-04-E13: crypto/rand error propagates as Go error (no panic/os.Exit)
	original := RandReader
	defer func() { RandReader = original }()

	RandReader = &failingReader{err: errors.New("mock rand failure")}
	_, err := generateBase62(8)
	if err == nil {
		t.Fatal("generateBase62 with failing reader = nil error; want error")
	}
	if !strings.Contains(err.Error(), "crypto/rand read error") {
		t.Errorf("error = %v; want message containing 'crypto/rand read error'", err)
	}
}

// failingReader is an io.Reader that always returns an error.
type failingReader struct {
	err error
}

func (f *failingReader) Read(p []byte) (int, error) {
	return 0, f.err
}

// ---------------------------------------------------------------------------
// GenerateTokenID tests
// ---------------------------------------------------------------------------

func TestGenerateTokenID_Format(t *testing.T) {
	tokenID, err := GenerateTokenID()
	if err != nil {
		t.Fatalf("GenerateTokenID() error: %v", err)
	}
	if len(tokenID) != 8 {
		t.Errorf("GenerateTokenID() length = %d, want 8", len(tokenID))
	}
	tokenIDRe := regexp.MustCompile(`^[0-9A-Za-z]{8}$`)
	if !tokenIDRe.MatchString(tokenID) {
		t.Errorf("GenerateTokenID() = %q; does not match base62 pattern", tokenID)
	}
}

func TestGenerateTokenID_ErrorPropagation(t *testing.T) {
	original := RandReader
	defer func() { RandReader = original }()

	RandReader = &failingReader{err: errors.New("rand error")}
	_, err := GenerateTokenID()
	if err == nil {
		t.Fatal("GenerateTokenID with failing reader = nil error; want error")
	}
}

// ---------------------------------------------------------------------------
// GenerateSecret tests
// ---------------------------------------------------------------------------

func TestGenerateSecret_Format(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret() error: %v", err)
	}
	if len(secret) != 32 {
		t.Errorf("GenerateSecret() length = %d, want 32", len(secret))
	}
	secretRe := regexp.MustCompile(`^[0-9A-Za-z]{32}$`)
	if !secretRe.MatchString(secret) {
		t.Errorf("GenerateSecret() = %q; does not match base62 pattern", secret)
	}
}

// ---------------------------------------------------------------------------
// HashSecret tests
// ---------------------------------------------------------------------------

func TestHashSecret_CorrectSHA256(t *testing.T) {
	// TS-04-50: verify hash matches expected SHA-256
	secret := "abcdefghABCDEFGH0123456789abcdef"
	expected := sha256.Sum256([]byte(secret))
	expectedHex := hex.EncodeToString(expected[:])

	got := HashSecret(secret)
	if got != expectedHex {
		t.Errorf("HashSecret(%q) = %q; want %q", secret, got, expectedHex)
	}
}

func TestHashSecret_NotPlaintext(t *testing.T) {
	secret := "mysecretvalue123"
	hash := HashSecret(secret)
	if hash == secret {
		t.Error("HashSecret should return a hash, not the plaintext secret")
	}
}

func TestHashSecret_DeterministicOutput(t *testing.T) {
	secret := "testdeterminism"
	h1 := HashSecret(secret)
	h2 := HashSecret(secret)
	if h1 != h2 {
		t.Error("HashSecret should produce deterministic output")
	}
}

func TestHashSecret_HexEncoded(t *testing.T) {
	// SHA-256 output is 64 hex characters.
	hash := HashSecret("anysecret")
	if len(hash) != 64 {
		t.Errorf("HashSecret length = %d; want 64 hex chars", len(hash))
	}
	hexRe := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if !hexRe.MatchString(hash) {
		t.Errorf("HashSecret = %q; not a valid 64-char lowercase hex string", hash)
	}
}

// ---------------------------------------------------------------------------
// AssembleToken tests
// ---------------------------------------------------------------------------

func TestAssembleToken_Format(t *testing.T) {
	// TS-04-49: token matches af_wt_<8 base62>_<32 base62>
	tokenID := "AbCd1234"
	secret := "abcdefghABCDEFGH0123456789abcdef"
	token := AssembleToken(tokenID, secret)
	expected := "af_wt_AbCd1234_abcdefghABCDEFGH0123456789abcdef"
	if token != expected {
		t.Errorf("AssembleToken = %q; want %q", token, expected)
	}
}

func TestAssembleToken_MatchesRegex(t *testing.T) {
	tokenID, err := GenerateTokenID()
	if err != nil {
		t.Fatalf("GenerateTokenID error: %v", err)
	}
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret error: %v", err)
	}
	token := AssembleToken(tokenID, secret)

	tokenRe := regexp.MustCompile(`^af_wt_[0-9A-Za-z]{8}_[0-9A-Za-z]{32}$`)
	if !tokenRe.MatchString(token) {
		t.Errorf("AssembleToken = %q; does not match expected regex", token)
	}
}

// ---------------------------------------------------------------------------
// NormalizeLabel tests
// ---------------------------------------------------------------------------

func TestNormalizeLabel_Nil(t *testing.T) {
	result, err := NormalizeLabel(nil)
	if err != nil {
		t.Fatalf("NormalizeLabel(nil) error: %v", err)
	}
	if result != nil {
		t.Errorf("NormalizeLabel(nil) = %v; want nil", result)
	}
}

func TestNormalizeLabel_EmptyString(t *testing.T) {
	// TS-04-34: empty string normalized to null
	s := ""
	result, err := NormalizeLabel(&s)
	if err != nil {
		t.Fatalf("NormalizeLabel(\"\") error: %v", err)
	}
	if result != nil {
		t.Errorf("NormalizeLabel(\"\") = %v; want nil", result)
	}
}

func TestNormalizeLabel_NonEmpty(t *testing.T) {
	// TS-04-34: non-empty label stored as-is
	s := "my-label"
	result, err := NormalizeLabel(&s)
	if err != nil {
		t.Fatalf("NormalizeLabel(%q) error: %v", s, err)
	}
	if result == nil || *result != "my-label" {
		t.Errorf("NormalizeLabel(%q) = %v; want pointer to %q", s, result, s)
	}
}

func TestNormalizeLabel_Max128(t *testing.T) {
	s := strings.Repeat("a", 128)
	result, err := NormalizeLabel(&s)
	if err != nil {
		t.Fatalf("NormalizeLabel(128 chars) error: %v", err)
	}
	if result == nil || *result != s {
		t.Error("NormalizeLabel(128 chars) should accept exactly 128 characters")
	}
}

func TestNormalizeLabel_Exceeds128(t *testing.T) {
	// TS-04-35: label >128 returns 400
	s := strings.Repeat("a", 129)
	_, err := NormalizeLabel(&s)
	if err == nil {
		t.Error("NormalizeLabel(129 chars) = nil error; want error for exceeding 128")
	}
}

// ---------------------------------------------------------------------------
// ValidateExpires tests
// ---------------------------------------------------------------------------

func TestValidateExpires_Nil(t *testing.T) {
	// TS-04-32: omitted defaults to 30
	result, err := ValidateExpires(nil)
	if err != nil {
		t.Fatalf("ValidateExpires(nil) error: %v", err)
	}
	if result != 30 {
		t.Errorf("ValidateExpires(nil) = %d; want 30", result)
	}
}

func TestValidateExpires_ValidValues(t *testing.T) {
	for _, v := range []int{0, 30, 60, 90} {
		val := v
		result, err := ValidateExpires(&val)
		if err != nil {
			t.Errorf("ValidateExpires(%d) error: %v", v, err)
		}
		if result != v {
			t.Errorf("ValidateExpires(%d) = %d; want %d", v, result, v)
		}
	}
}

func TestValidateExpires_InvalidValues(t *testing.T) {
	// TS-04-33: invalid expires values
	invalid := []int{1, 29, 31, 45, 91, 100, -1}
	for _, v := range invalid {
		val := v
		_, err := ValidateExpires(&val)
		if err == nil {
			t.Errorf("ValidateExpires(%d) = nil error; want error for invalid value", v)
		}
	}
}

// ---------------------------------------------------------------------------
// ComputeExpiresAt tests
// ---------------------------------------------------------------------------

func TestComputeExpiresAt_Zero(t *testing.T) {
	// TS-04-32: expires=0 → null
	now := time.Now().UTC()
	result := ComputeExpiresAt(now, 0)
	if result != nil {
		t.Errorf("ComputeExpiresAt(now, 0) = %v; want nil", result)
	}
}

func TestComputeExpiresAt_ThirtyDays(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	result := ComputeExpiresAt(now, 30)
	if result == nil {
		t.Fatal("ComputeExpiresAt(now, 30) = nil; want non-nil")
	}
	parsed, err := time.Parse(time.RFC3339, *result)
	if err != nil {
		t.Fatalf("failed to parse expires_at %q: %v", *result, err)
	}
	expected := now.AddDate(0, 0, 30)
	if !parsed.Equal(expected) {
		t.Errorf("ComputeExpiresAt = %v; want %v", parsed, expected)
	}
}

func TestComputeExpiresAt_SixtyDays(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	result := ComputeExpiresAt(now, 60)
	if result == nil {
		t.Fatal("ComputeExpiresAt(now, 60) = nil; want non-nil")
	}
	parsed, err := time.Parse(time.RFC3339, *result)
	if err != nil {
		t.Fatalf("failed to parse expires_at %q: %v", *result, err)
	}
	expected := now.AddDate(0, 0, 60)
	if !parsed.Equal(expected) {
		t.Errorf("ComputeExpiresAt = %v; want %v", parsed, expected)
	}
}

func TestComputeExpiresAt_NinetyDays(t *testing.T) {
	now := time.Date(2026, 3, 15, 8, 30, 0, 0, time.UTC)
	result := ComputeExpiresAt(now, 90)
	if result == nil {
		t.Fatal("ComputeExpiresAt(now, 90) = nil; want non-nil")
	}
	parsed, err := time.Parse(time.RFC3339, *result)
	if err != nil {
		t.Fatalf("failed to parse expires_at %q: %v", *result, err)
	}
	expected := now.AddDate(0, 0, 90)
	if !parsed.Equal(expected) {
		t.Errorf("ComputeExpiresAt = %v; want %v", parsed, expected)
	}
}

// ---------------------------------------------------------------------------
// End-to-end token generation test
// ---------------------------------------------------------------------------

func TestTokenGeneration_EndToEnd(t *testing.T) {
	// Generate a full workspace token and verify the whole pipeline.
	tokenID, err := GenerateTokenID()
	if err != nil {
		t.Fatalf("GenerateTokenID error: %v", err)
	}
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret error: %v", err)
	}

	// Assemble the full token.
	fullToken := AssembleToken(tokenID, secret)

	// Verify the format.
	tokenRe := regexp.MustCompile(`^af_wt_[0-9A-Za-z]{8}_[0-9A-Za-z]{32}$`)
	if !tokenRe.MatchString(fullToken) {
		t.Errorf("fullToken = %q; does not match expected pattern", fullToken)
	}

	// Verify we can parse the token back.
	parts := strings.Split(fullToken, "_")
	if len(parts) != 4 {
		t.Fatalf("token has %d parts; want 4", len(parts))
	}
	if parts[0] != "af" || parts[1] != "wt" {
		t.Errorf("token prefix = %s_%s; want af_wt", parts[0], parts[1])
	}
	parsedTokenID := parts[2]
	parsedSecret := parts[3]

	if parsedTokenID != tokenID {
		t.Errorf("parsed token_id = %q; want %q", parsedTokenID, tokenID)
	}
	if parsedSecret != secret {
		t.Errorf("parsed secret = %q; want %q", parsedSecret, secret)
	}

	// Hash the secret and verify it doesn't match plaintext.
	hash := HashSecret(secret)
	if hash == secret {
		t.Error("hash should not equal plaintext secret")
	}

	// Verify hash is the correct SHA-256.
	expectedHash := sha256.Sum256([]byte(secret))
	expectedHex := hex.EncodeToString(expectedHash[:])
	if hash != expectedHex {
		t.Errorf("HashSecret = %q; want %q", hash, expectedHex)
	}
}

// Ensure failingReader implements io.Reader at compile time.
var _ io.Reader = (*failingReader)(nil)
