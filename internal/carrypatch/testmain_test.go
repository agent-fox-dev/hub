package carrypatch

import (
	"os"
	"testing"
)

// TestMain pins git's default branch for every fixture in this package. The
// fixtures init bare repos and clone empty ones without naming the branch,
// then `git push origin main`; that only works when init.defaultBranch is
// main, which a stock git (default branch `master`) does not set.
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_COUNT", "1")
	os.Setenv("GIT_CONFIG_KEY_0", "init.defaultBranch")
	os.Setenv("GIT_CONFIG_VALUE_0", "main")
	os.Exit(m.Run())
}
