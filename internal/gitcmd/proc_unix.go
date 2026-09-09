//go:build unix

package gitcmd

import (
	"os/exec"
	"syscall"
	"time"
)

// configureProcess places the git subprocess in its own process group and
// makes context cancellation kill the whole group. git spawns helpers
// (credential helpers, hooks, pagers, ssh) that would otherwise survive the
// parent and keep the stdout/stderr pipes open, which in turn blocks
// cmd.Wait indefinitely; WaitDelay bounds that wait as a last resort.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid addresses the process group created by Setpgid.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = killWaitDelay
}

// killWaitDelay is how long cmd.Wait keeps waiting for pipe readers after the
// process has been killed before giving up on stragglers.
const killWaitDelay = 5 * time.Second
