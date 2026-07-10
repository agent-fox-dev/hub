package main

import (
	"fmt"
	"os"

	afccmd "github.com/agentfox/hub/internal/cmd"
)

// version is injected at build time via -ldflags.
var version = "0.1.0"

func main() {
	rootCmd := afccmd.NewRootCmd(version)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
