package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

func main() {
	rootCmd := apikit.RootCommand()
	rootCmd.Use = "mycli"
	rootCmd.Short = "My custom CLI built on apikit"

	// Register all built-in commands.
	rootCmd.AddCommand(
		apikit.LoginCmd(),
		apikit.UserCmd(),
		apikit.KeysCmd(),
		apikit.TokensCmd(),
		apikit.OrgsCmd(),
		apikit.AdminCmd(),
	)

	// Add your own commands alongside the built-in ones.
	rootCmd.AddCommand(&cobra.Command{
		Use:   "hello",
		Short: "Print a greeting",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "Hello from mycli!")
			return nil
		},
	})

	err := apikit.CLIExecute()
	if err != nil {
		apikit.CLIPrintError(err)
	}
	os.Exit(apikit.CLIExitCode(err))
}
