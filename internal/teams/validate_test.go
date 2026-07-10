package teams

import (
	"strings"
	"testing"
)

// --- ValidateName tests ---

func TestValidateName_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"single char", "A"},
		{"normal name", "Engineering"},
		{"max length 255", strings.Repeat("a", 255)},
		{"with spaces", "My Great Team"},
		{"with special chars", "Team #1 (Alpha)"},
		{"unicode", "équipe développement"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateName(tt.input); err != nil {
				t.Errorf("ValidateName(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}

func TestValidateName_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"exceeds 255 chars", strings.Repeat("a", 256)},
		{"way too long", strings.Repeat("b", 1000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.input)
			if err == nil {
				t.Errorf("ValidateName(%q) = nil, want ErrInvalidTeamName", tt.input)
			}
			if err != ErrInvalidTeamName {
				t.Errorf("ValidateName(%q) = %v, want ErrInvalidTeamName", tt.input, err)
			}
		})
	}
}

// --- ValidateSlug tests ---

func TestValidateSlug_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"min length 3", "abc"},
		{"typical slug", "my-team"},
		{"all digits except first", "a12"},
		{"max length 64", "a" + strings.Repeat("b", 62) + "c"},
		{"consecutive hyphens", "my--team"},
		{"starts letter ends digit", "team1"},
		{"all lowercase letters", "abcdefghij"},
		{"mixed letters digits hyphens", "team-alpha-42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateSlug(tt.input); err != nil {
				t.Errorf("ValidateSlug(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}

func TestValidateSlug_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"too short 1 char", "a"},
		{"too short 2 chars", "ab"},
		{"starts with digit", "1team"},
		{"starts with hyphen", "-team"},
		{"ends with hyphen", "team-"},
		{"uppercase letters", "My-Team"},
		{"contains underscore", "my_team"},
		{"contains space", "my team"},
		{"contains dot", "my.team"},
		{"too long 65 chars", "a" + strings.Repeat("b", 63) + "c"},
		{"single letter", "a"},
		{"starts with digit ends with digit", "123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSlug(tt.input)
			if err == nil {
				t.Errorf("ValidateSlug(%q) = nil, want ErrInvalidSlugFormat", tt.input)
			}
			if err != ErrInvalidSlugFormat {
				t.Errorf("ValidateSlug(%q) = %v, want ErrInvalidSlugFormat", tt.input, err)
			}
		})
	}
}

// TestValidateSlug_BoundaryLengths tests exact boundary lengths for slug validation.
func TestValidateSlug_BoundaryLengths(t *testing.T) {
	// Length 2: too short (minimum is 3).
	slug2 := "ab"
	if err := ValidateSlug(slug2); err != ErrInvalidSlugFormat {
		t.Errorf("slug length 2 (%q): got %v, want ErrInvalidSlugFormat", slug2, err)
	}

	// Length 3: minimum valid length.
	slug3 := "abc"
	if err := ValidateSlug(slug3); err != nil {
		t.Errorf("slug length 3 (%q): got %v, want nil", slug3, err)
	}

	// Length 64: maximum valid length.
	// ^[a-z][a-z0-9-]{1,62}[a-z0-9]$ → 1 + 62 + 1 = 64
	slug64 := "a" + strings.Repeat("b", 62) + "c"
	if len(slug64) != 64 {
		t.Fatalf("expected slug64 length 64, got %d", len(slug64))
	}
	if err := ValidateSlug(slug64); err != nil {
		t.Errorf("slug length 64 (%q): got %v, want nil", slug64, err)
	}

	// Length 65: too long.
	slug65 := "a" + strings.Repeat("b", 63) + "c"
	if len(slug65) != 65 {
		t.Fatalf("expected slug65 length 65, got %d", len(slug65))
	}
	if err := ValidateSlug(slug65); err != ErrInvalidSlugFormat {
		t.Errorf("slug length 65 (%q): got %v, want ErrInvalidSlugFormat", slug65, err)
	}
}

// --- ValidateURL tests ---

func TestValidateURL_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"http url", "http://example.com"},
		{"https url", "https://example.com"},
		{"https with path", "https://example.com/path/to/page"},
		{"https with port", "https://example.com:8080"},
		{"https with query", "https://example.com?q=test"},
		{"https with fragment", "https://example.com#section"},
		{"http with subdomain", "http://www.example.com"},
		{"https with ip", "https://192.168.1.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.input); err != nil {
				t.Errorf("ValidateURL(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}

func TestValidateURL_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"ftp scheme", "ftp://example.com"},
		{"no scheme", "example.com"},
		{"empty scheme", "://example.com"},
		{"mailto scheme", "mailto:user@example.com"},
		{"javascript scheme", "javascript:alert(1)"},
		{"file scheme", "file:///etc/passwd"},
		{"no host http", "http://"},
		{"no host https", "https://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateURL(tt.input)
			if err == nil {
				t.Errorf("ValidateURL(%q) = nil, want ErrInvalidURLFormat", tt.input)
			}
			if err != ErrInvalidURLFormat {
				t.Errorf("ValidateURL(%q) = %v, want ErrInvalidURLFormat", tt.input, err)
			}
		})
	}
}

// --- validateUUID tests ---

func TestValidateUUID_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"standard uuid v4", "550e8400-e29b-41d4-a716-446655440000"},
		{"nil uuid", "00000000-0000-0000-0000-000000000000"},
		{"uppercase uuid", "550E8400-E29B-41D4-A716-446655440000"},
		{"mixed case uuid", "550e8400-E29B-41d4-A716-446655440000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateUUID(tt.input); err != nil {
				t.Errorf("validateUUID(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}

func TestValidateUUID_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"not a uuid", "not-a-uuid"},
		{"plain text", "hello"},
		{"short hex", "550e8400"},
		{"missing segment", "550e8400-e29b-41d4-a716"},
		{"extra segment", "550e8400-e29b-41d4-a716-446655440000-extra"},
		{"invalid chars", "gggggggg-gggg-gggg-gggg-gggggggggggg"},
		{"spaces", "550e8400 e29b 41d4 a716 446655440000"},
		{"numeric only", "12345"},
		{"path-like", "bad-id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUUID(tt.input)
			if err == nil {
				t.Errorf("validateUUID(%q) = nil, want ErrInvalidIDFormat", tt.input)
			}
			if err != ErrInvalidIDFormat {
				t.Errorf("validateUUID(%q) = %v, want ErrInvalidIDFormat", tt.input, err)
			}
		})
	}
}
