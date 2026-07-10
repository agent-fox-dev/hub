package cmd_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	afccmd "github.com/agent-fox-dev/hub/internal/cmd"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// cmdResult captures the output and exit status of a CLI invocation.
type cmdResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// runCLI creates a root Cobra command and executes it with the given args,
// capturing stdout and stderr. Returns exit code 0 on success, 1 on error.
func runCLI(t *testing.T, args []string) cmdResult {
	t.Helper()

	rootCmd := afccmd.NewRootCmd("0.1.0")
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SetArgs(args)

	// Silence usage output on error so it doesn't pollute stderr assertions.
	// Note: Cobra still prints usage for some errors (like missing args).
	// We do NOT silence usage globally because some tests (TS-05-39) verify
	// that Cobra prints usage errors.

	err := rootCmd.Execute()

	exitCode := 0
	if err != nil {
		exitCode = 1
	}

	return cmdResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Err:      err,
	}
}

// ---------------------------------------------------------------------------
// 1.4 — Root Command, Version Flag, and Flag Validation Tests (REQ-4, REQ-5)
// ---------------------------------------------------------------------------

// TestRootCommandHelp verifies that invoking afc with no subcommand prints
// help text to stdout and exits with code 0.
// TS-05-9
func TestRootCommandHelp(t *testing.T) {
	result := runCLI(t, []string{})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}

	if !strings.Contains(result.Stdout, "Usage:") {
		t.Errorf("stdout should contain 'Usage:', got: %q", result.Stdout)
	}

	// Help text should list available subcommands.
	if !strings.Contains(result.Stdout, "login") {
		t.Errorf("stdout should list 'login' subcommand, got: %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "workspace") {
		t.Errorf("stdout should list 'workspace' subcommand, got: %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "keys") {
		t.Errorf("stdout should list 'keys' subcommand, got: %q", result.Stdout)
	}
}

// TestVersionFlag verifies that afc --version prints 'afc version 0.1.0'
// to stdout and exits with code 0.
// TS-05-10
func TestVersionFlag(t *testing.T) {
	result := runCLI(t, []string{"--version"})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}

	want := "afc version 0.1.0"
	if !strings.Contains(result.Stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", result.Stdout, want)
	}
}

// TestHelpCommand verifies that --help and 'afc help <command>' produce help
// text on stdout with exit code 0.
// TS-05-11
func TestHelpCommand(t *testing.T) {
	// Test --help on root command.
	t.Run("root --help", func(t *testing.T) {
		result := runCLI(t, []string{"--help"})

		if result.ExitCode != 0 {
			t.Errorf("exit code = %d, want 0", result.ExitCode)
		}
		if !strings.Contains(result.Stdout, "Usage:") {
			t.Errorf("stdout should contain 'Usage:', got: %q", result.Stdout)
		}
	})

	// Test 'afc help login'.
	t.Run("help login", func(t *testing.T) {
		result := runCLI(t, []string{"help", "login"})

		if result.ExitCode != 0 {
			t.Errorf("exit code = %d, want 0", result.ExitCode)
		}
		if !strings.Contains(result.Stdout, "login") {
			t.Errorf("stdout should contain 'login', got: %q", result.Stdout)
		}
		if !strings.Contains(result.Stdout, "--provider") {
			t.Errorf("stdout should show --provider flag, got: %q", result.Stdout)
		}
	})

	// Test 'afc help workspace'.
	t.Run("help workspace", func(t *testing.T) {
		result := runCLI(t, []string{"help", "workspace"})

		if result.ExitCode != 0 {
			t.Errorf("exit code = %d, want 0", result.ExitCode)
		}
		if !strings.Contains(result.Stdout, "workspace") {
			t.Errorf("stdout should contain 'workspace', got: %q", result.Stdout)
		}
	})

	// Test 'afc help keys'.
	t.Run("help keys", func(t *testing.T) {
		result := runCLI(t, []string{"help", "keys"})

		if result.ExitCode != 0 {
			t.Errorf("exit code = %d, want 0", result.ExitCode)
		}
		if !strings.Contains(result.Stdout, "keys") {
			t.Errorf("stdout should contain 'keys', got: %q", result.Stdout)
		}
	})
}

// TestInvalidExpiresFlag verifies that --expires with a value not in
// {0, 30, 60, 90} causes the CLI to print the exact error message to stderr
// and exit with code 1 before any network request.
// TS-05-12
func TestInvalidExpiresFlag(t *testing.T) {
	t.Run("login --expires 45", func(t *testing.T) {
		result := runCLI(t, []string{"login", "--provider", "github", "--expires", "45"})

		if result.ExitCode != 1 {
			t.Errorf("exit code = %d, want 1", result.ExitCode)
		}

		wantMsg := "Error: --expires must be one of: 0, 30, 60, 90"
		if !strings.Contains(result.Stderr, wantMsg) {
			t.Errorf("stderr = %q, want it to contain %q", result.Stderr, wantMsg)
		}
	})

	// Also test for workspace token create.
	t.Run("workspace token create --expires 45", func(t *testing.T) {
		result := runCLI(t, []string{"workspace", "token", "create", "--workspace", "ws", "--expires", "45"})

		if result.ExitCode != 1 {
			t.Errorf("exit code = %d, want 1", result.ExitCode)
		}

		wantMsg := "Error: --expires must be one of: 0, 30, 60, 90"
		if !strings.Contains(result.Stderr, wantMsg) {
			t.Errorf("stderr = %q, want it to contain %q", result.Stderr, wantMsg)
		}
	})
}

// TestEmptyGitURLFlag verifies that providing --git-url as an empty string
// for workspace create causes the CLI to print a descriptive error and exit
// with code 1 before any network request.
// TS-05-13
func TestEmptyGitURLFlag(t *testing.T) {
	result := runCLI(t, []string{"workspace", "create", "--git-url", "", "--slug", "my-ws"})

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}

	// Error should mention the flag name.
	if !strings.Contains(result.Stderr, "git-url") && !strings.Contains(result.Stderr, "--git-url") {
		t.Errorf("stderr should mention --git-url, got: %q", result.Stderr)
	}
}

// TestEmptyWorkspaceFlag verifies that providing --workspace as an empty
// string for workspace token commands causes the CLI to print a descriptive
// error and exit with code 1 before any network request.
// TS-05-14
func TestEmptyWorkspaceFlag(t *testing.T) {
	t.Run("workspace token list", func(t *testing.T) {
		result := runCLI(t, []string{"workspace", "token", "list", "--workspace", ""})

		if result.ExitCode != 1 {
			t.Errorf("exit code = %d, want 1", result.ExitCode)
		}

		if !strings.Contains(result.Stderr, "workspace") && !strings.Contains(result.Stderr, "--workspace") {
			t.Errorf("stderr should mention --workspace, got: %q", result.Stderr)
		}
	})

	t.Run("workspace token create", func(t *testing.T) {
		result := runCLI(t, []string{"workspace", "token", "create", "--workspace", ""})

		if result.ExitCode != 1 {
			t.Errorf("exit code = %d, want 1", result.ExitCode)
		}

		if !strings.Contains(result.Stderr, "workspace") && !strings.Contains(result.Stderr, "--workspace") {
			t.Errorf("stderr should mention --workspace, got: %q", result.Stderr)
		}
	})

	t.Run("workspace token revoke", func(t *testing.T) {
		result := runCLI(t, []string{"workspace", "token", "revoke", "--workspace", "", "tok-id"})

		if result.ExitCode != 1 {
			t.Errorf("exit code = %d, want 1", result.ExitCode)
		}

		if !strings.Contains(result.Stderr, "workspace") && !strings.Contains(result.Stderr, "--workspace") {
			t.Errorf("stderr should mention --workspace, got: %q", result.Stderr)
		}
	})
}

// TestWorkspaceTokenRevokeMissingTokenID verifies that omitting the <token-id>
// positional argument for workspace token revoke causes Cobra's ExactArgs(1)
// to print a usage error and exit with code 1.
// TS-05-39
func TestWorkspaceTokenRevokeMissingTokenID(t *testing.T) {
	result := runCLI(t, []string{"workspace", "token", "revoke", "--workspace", "ws1"})

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}

	// Cobra's ExactArgs(1) should produce a usage error about wrong number
	// of arguments.
	if !strings.Contains(result.Stderr, "accepts 1 arg") &&
		!strings.Contains(result.Stderr, "argument") &&
		!strings.Contains(result.Err.Error(), "accepts 1 arg") {
		t.Errorf("stderr or error should mention wrong number of arguments, got stderr=%q, err=%v",
			result.Stderr, result.Err)
	}
}

// TestPropertyExpiresValidation is a property test that verifies for any
// --expires value not in {0, 30, 60, 90}, the CLI exits with code 1 before
// issuing any HTTP request, for both afc login and afc workspace token create.
// TS-05-P6
func TestPropertyExpiresValidation(t *testing.T) {
	validValues := map[int]bool{0: true, 30: true, 60: true, 90: true}

	// Test a representative range of invalid values.
	invalidValues := []int{-5, -1, 1, 10, 15, 20, 29, 31, 45, 59, 61, 89, 91, 100, 120}

	for _, v := range invalidValues {
		t.Run(fmt.Sprintf("login_expires_%d", v), func(t *testing.T) {
			if validValues[v] {
				t.Skip("value is valid, skipping")
			}

			result := runCLI(t, []string{
				"login", "--provider", "github", "--expires", fmt.Sprintf("%d", v),
			})

			if result.ExitCode != 1 {
				t.Errorf("login --expires %d: exit code = %d, want 1", v, result.ExitCode)
			}

			wantMsg := "Error: --expires must be one of: 0, 30, 60, 90"
			if !strings.Contains(result.Stderr, wantMsg) {
				t.Errorf("login --expires %d: stderr = %q, want it to contain %q", v, result.Stderr, wantMsg)
			}
		})

		t.Run(fmt.Sprintf("token_create_expires_%d", v), func(t *testing.T) {
			if validValues[v] {
				t.Skip("value is valid, skipping")
			}

			result := runCLI(t, []string{
				"workspace", "token", "create", "--workspace", "ws", "--expires", fmt.Sprintf("%d", v),
			})

			if result.ExitCode != 1 {
				t.Errorf("workspace token create --expires %d: exit code = %d, want 1", v, result.ExitCode)
			}

			wantMsg := "Error: --expires must be one of: 0, 30, 60, 90"
			if !strings.Contains(result.Stderr, wantMsg) {
				t.Errorf("workspace token create --expires %d: stderr = %q, want it to contain %q", v, result.Stderr, wantMsg)
			}
		})
	}

	// Also verify that valid values do NOT produce this error.
	for v := range validValues {
		t.Run(fmt.Sprintf("login_valid_expires_%d", v), func(t *testing.T) {
			result := runCLI(t, []string{
				"login", "--provider", "github", "--expires", fmt.Sprintf("%d", v),
			})

			forbiddenMsg := "Error: --expires must be one of: 0, 30, 60, 90"
			if strings.Contains(result.Stderr, forbiddenMsg) {
				t.Errorf("login --expires %d should not produce expires validation error", v)
			}
		})
	}
}
