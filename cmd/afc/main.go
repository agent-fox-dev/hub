package main

import (
	"fmt"
	"os"

	afccmd "github.com/agent-fox-dev/hub/internal/cmd"
)

// version is injected at build time via -ldflags.
var Version = "dev"

func main() {
	rootCmd := afccmd.NewRootCmd(Version)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
