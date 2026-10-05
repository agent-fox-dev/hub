package main

import (
	"os"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/cli"
)

func main() {
	// Build the full command tree: apikit standard commands + workspace commands.
	// BuildRootCommand calls apikit.RootCommand() which stores the root command
	// internally, so CLIExecute() will operate on the fully-configured tree.
	_ = cli.BuildRootCommand()

	// Execute the command tree using apikit's centralized execution.
	err := apikit.CLIExecute()
	// The --fail-on-diverged error has already been reported (response on
	// stdout, branch names on stderr); printing it would add a second JSON
	// document to stdout.
	if err != nil && !cli.IsDivergedError(err) {
		apikit.CLIPrintError(err)
	}
	// cli.ExitCode is apikit.CLIExitCode plus exit 3 for --fail-on-diverged,
	// which apikit would otherwise map to 2.
	os.Exit(cli.ExitCode(err))
}
