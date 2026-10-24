package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/txsvc/apikit"
)

// ExitCode maps the --fail-on-diverged error to 3 and leaves apikit's codes
// (0 success, 1 API error, 2 client error) as they are.
func TestExitCode_MapsDivergedToThreeAndKeepsAPIKitCodes(t *testing.T) {
	for _, tc := range []struct {
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
	} {
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
