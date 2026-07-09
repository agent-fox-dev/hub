package validator

import (
	"strings"
	"testing"
)

// TS-07-27: Verifies the slug validator accepts slugs that start with a letter
// and contain only lowercase alphanumeric characters and hyphens, with length
// 3-64.
//
// Requirement: 07-REQ-6.1
func TestValidateSlug_ValidInputs(t *testing.T) {
	tests := []struct {
		name string
		slug string
	}{
		{"three_chars_min_length", "abc"},
		{"with_hyphen", "my-api"},
		{"letter_then_digits", "a12"},
		{"multiple_hyphen_segments", "a-b-c"},
		{"workspace_style_slug", "my-workspace-123"},
		{"max_length_64", "a" + strings.Repeat("b", 63)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !ValidateSlug(tt.slug) {
				t.Errorf("ValidateSlug(%q) = false, want true", tt.slug)
			}
		})
	}
}

// TS-07-28: Verifies the slug validator rejects slugs starting with a digit or
// hyphen, containing uppercase letters, consecutive hyphens, trailing hyphens,
// or with length outside 3-64.
//
// TS-07-E11: Verifies the slug validator returns false without panicking when
// an empty string is passed.
//
// Requirements: 07-REQ-6.2, 07-REQ-6.E1
func TestValidateSlug_InvalidInputs(t *testing.T) {
	tests := []struct {
		name string
		slug string
	}{
		{"starts_with_digit", "1abc"},
		{"starts_with_hyphen", "-abc"},
		{"contains_uppercase", "MyApi"},
		{"consecutive_hyphens", "my--api"},
		{"trailing_hyphen", "my-api-"},
		{"too_short_2_chars", "ab"},
		{"empty_string", ""},
		{"too_long_65_chars", strings.Repeat("a", 65)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ValidateSlug(tt.slug) {
				t.Errorf("ValidateSlug(%q) = true, want false", tt.slug)
			}
		})
	}
}

// TestValidateSlug_EmptyStringNoPanic is an explicit edge case test ensuring
// that an empty string does not cause a panic.
//
// TS-07-E11: Requirement 07-REQ-6.E1
func TestValidateSlug_EmptyStringNoPanic(t *testing.T) {
	// This test verifies that the function returns false and does not panic.
	// If the function panics, the test will fail with a panic trace.
	result := ValidateSlug("")
	if result {
		t.Error("ValidateSlug(\"\") = true, want false")
	}
}
