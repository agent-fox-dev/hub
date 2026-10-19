package cli

import (
	"net/http"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// SandboxCmd returns the 'sandbox' parent cobra.Command with the subcommand
// that lists the deployment's sandbox profiles.
//
// The authenticated CLI client is retrieved from the Cobra context via
// apikit.CLIClientFromCmd — credentials are resolved by apikit's
// PersistentPreRunE from flags, environment variables, and the config file.
func SandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "sandbox",
		Short:         "Work with sandboxes",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(newSandboxProfilesCmd())

	return cmd
}

// newSandboxProfilesCmd returns the 'sandbox profiles' subcommand.
// It sends GET /api/v1/sandbox-profiles and prints the response, the list of
// the deployment's sandbox profiles, as one JSON document.
// Requirements: 25-REQ-9.1, 25-REQ-9.2, 25-REQ-9.3
func newSandboxProfilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "profiles",
		Short:         "List the sandbox profiles of the hub",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet,
				apiPath("sandbox-profiles"), nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}
