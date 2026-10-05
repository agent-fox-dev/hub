package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/txsvc/apikit"
)

// TestExitCode pins the process exit-code mapping: the --fail-on-diverged
// code 3 is produced hub-side, and the apikit codes 0/1/2 are unchanged.
//
// Verifies: 20-REQ-6.5
func TestExitCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		want     int
		diverged bool
	}{
		{"nil", nil, 0, false},
		{"diverged", apikit.NewCLIError(ExitCodeDiverged, "diverged patch branches: a"), 3, true},
		{"diverged wrapped", fmt.Errorf("wrapped: %w", apikit.NewCLIError(3, "x")), 3, true},
		{"client error", apikit.NewCLIError(2, "bad flag"), 2, false},
		{"api error code 1", apikit.NewCLIError(1, "job failed"), 1, false},
		{"http status code", apikit.NewCLIError(404, "not found"), 1, false},
		{"plain error", errors.New("boom"), 2, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode = %d; want %d", got, tc.want)
			}
			if got := IsDivergedError(tc.err); got != tc.diverged {
				t.Errorf("IsDivergedError = %v; want %v", got, tc.diverged)
			}
		})
	}
}
