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
	if err != nil {
		apikit.CLIPrintError(err)
	}
	os.Exit(apikit.CLIExitCode(err))
}
