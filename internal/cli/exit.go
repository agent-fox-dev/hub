package cli

import (
	"errors"

	"github.com/txsvc/apikit"
)

// ExitCodeDiverged is the process exit code of 'afc workspace sync
// --fail-on-diverged' when the sync response lists diverged patch branches.
// Exit codes 1 (API / wait failure) and 2 (client error) keep their meaning.
//
// Requirements: 20-REQ-6.5
const ExitCodeDiverged = 3

// ExitCode maps an error returned by the command tree to the process exit
// code. It is apikit.CLIExitCode plus one hub-specific code: a CLIError
// carrying ExitCodeDiverged exits 3. apikit collapses every CLIError code
// other than 1 and >= 400 to 2, so without this mapping the documented
// --fail-on-diverged exit code 3 never reaches the shell.
//
// Requirements: 20-REQ-6.5
func ExitCode(err error) int {
	if IsDivergedError(err) {
		return ExitCodeDiverged
	}
	return apikit.CLIExitCode(err)
}

// IsDivergedError reports whether err is the --fail-on-diverged failure
// returned by 'workspace sync'. The command has already written the sync
// response to stdout and the branch names to stderr when it returns this
// error, so the caller must not print it again.
func IsDivergedError(err error) bool {
	var ce *apikit.CLIError
	return errors.As(err, &ce) && ce.ErrorCode() == ExitCodeDiverged
}
