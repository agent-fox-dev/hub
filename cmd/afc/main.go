package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

func main() {
	// Start with apikit's base command tree (version, help).
	rootCmd := apikit.RootCommand()
	rootCmd.Use = "mycli"
	rootCmd.Short = "My custom CLI built on apikit"

	// Add your own commands alongside the built-in ones.
	rootCmd.AddCommand(&cobra.Command{
		Use:   "hello",
		Short: "Print a greeting",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "Hello from mycli!")
			return nil
		},
	})

	// The built-in akc commands (login, user, keys, tokens, orgs, admin)
	// are available via the akc binary. To embed them in your own CLI,
	// copy cmd/akc/main.go and adjust the imports.

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
