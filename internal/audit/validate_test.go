package audit

import "testing"

// TS-17-14: ValidateRunID returns true for valid run_id strings and
// false for invalid ones.
func TestValidateRunID_ValidFormats(t *testing.T) {
	valid := []string{
		"20260704_143022_a1b2c3",
		"20260101_000000_000000",
		"20261231_235959_abcdef",
		"20260901_120000_fedcba",
		"99991231_235959_ffffff",
	}
	for _, id := range valid {
		if !ValidateRunID(id) {
			t.Errorf("ValidateRunID(%q) = false, want true", id)
		}
	}
}

// TS-17-14 (invalid cases): Various invalid run_id formats are rejected.
func TestValidateRunID_InvalidFormats(t *testing.T) {
	cases := []struct {
		name  string
		runID string
	}{
		{"uppercase hex suffix", "20260704_143022_A1B2C3"},
		{"mixed case hex", "20260704_143022_a1B2c3"},
		{"empty string", ""},
		{"non-hex chars in suffix", "20260704_143022_xyz123"},
		{"short date", "2026070_143022_a1b2c3"},
		{"missing first underscore", "20260704143022_a1b2c3"},
		{"missing second underscore", "20260704_143022a1b2c3"},
		{"too short hex suffix", "20260704_143022_a1b2c"},
		{"too long hex suffix", "20260704_143022_a1b2c3d"},
		{"spaces", "20260704 143022 a1b2c3"},
		{"dashes instead of underscores", "20260704-143022-a1b2c3"},
		{"extra prefix", "run_20260704_143022_a1b2c3"},
		{"hex digit g", "20260704_143022_g1b2c3"},
		{"short time", "20260704_14302_a1b2c3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ValidateRunID(tc.runID) {
				t.Errorf("ValidateRunID(%q) = true, want false", tc.runID)
			}
		})
	}
}
