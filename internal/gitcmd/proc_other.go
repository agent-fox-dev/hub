//go:build !unix

package gitcmd

import (
	"os/exec"
	"time"
)

// configureProcess is the portable fallback: only the git process itself is
// killed on cancellation, and cmd.Wait is bounded by WaitDelay.
func configureProcess(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
