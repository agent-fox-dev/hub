package cli

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// RebuildCmd returns the 'rebuild' parent cobra.Command with subcommands
// for submit, list, and status of rebuild jobs.
//
// The authenticated CLI client is retrieved from the Cobra context via
// apikit.CLIClientFromCmd — credentials are resolved by apikit's
// PersistentPreRunE from flags, environment variables, and the config file.
func RebuildCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "rebuild",
		Short:         "Manage rebuild jobs",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newRebuildSubmitCmd(),
		newRebuildListCmd(),
		newRebuildStatusCmd(),
		newRebuildPreviewCmd(),
		newRebuildCancelCmd(),
		newRebuildRequeueCmd(),
		newRebuildRollbackCmd(),
	)

	return cmd
}

// newRebuildSubmitCmd returns the 'rebuild submit' subcommand.
// It sends POST /api/v1/workspaces/:slug/rebuild and prints the job record.
// With --wait, it polls the rebuild status until a terminal state is reached.
// Requirements: 16-REQ-7.1
func newRebuildSubmitCmd() *cobra.Command {
	var wf waitFlags
	var strategy string

	cmd := &cobra.Command{
		Use:           "submit <workspace-slug>",
		Short:         "Submit a rebuild job",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			// Build request body: nil when no flags, map with strategy when set.
			var body any
			if strategy != "" {
				body = map[string]any{
					"strategy": strategy,
				}
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost,
				"/workspaces/"+slug+"/rebuild", body)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			if !wf.Wait {
				return apikit.CLIPrintResult(cmd, result)
			}

			// Print the initial submit response, then poll for completion.
			printJSON(cmd, result)

			jobID, err := extractJobID(result)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			statusPath := "/workspaces/" + slug + "/rebuilds/" + jobID
			if err := pollJobStatus(cmd, client, wf, statusPath); err != nil {
				return apikit.CLIHandleError(cmd, err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&strategy, "strategy", "", "rebuild strategy override (rebase|merge)")
	addWaitFlags(cmd, &wf)
	return cmd
}

// newRebuildListCmd returns the 'rebuild list' subcommand.
// It sends GET /api/v1/workspaces/:slug/rebuilds and prints the list.
// Requirements: 16-REQ-7.2
func newRebuildListCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "list <workspace-slug>",
		Short:         "List rebuild jobs",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet,
				"/workspaces/"+args[0]+"/rebuilds", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newRebuildStatusCmd returns the 'rebuild status' subcommand.
// It sends GET /api/v1/workspaces/:slug/rebuilds/:id and prints job details.
// Requirements: 16-REQ-7.3
func newRebuildStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "status <workspace-slug> <rebuild-id>",
		Short:         "Get rebuild job status",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet,
				"/workspaces/"+args[0]+"/rebuilds/"+args[1], nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newRebuildPreviewCmd returns the 'rebuild preview' subcommand.
// It sends GET /api/v1/workspaces/:slug/rebuild-preview and prints the
// per-patch conflict prediction to stdout.
func newRebuildPreviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "preview <workspace-slug>",
		Short:         "Preview rebuild conflicts without executing",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet,
				"/workspaces/"+args[0]+"/rebuild-preview", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newRebuildCancelCmd returns the 'rebuild cancel' subcommand.
// It sends DELETE /api/v1/workspaces/:slug/rebuilds/:id and prints confirmation.
func newRebuildCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "cancel <workspace-slug> <rebuild-id>",
		Short:         "Cancel a rebuild job",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			_, err = client.DoRequest(cmd.Context(), http.MethodDelete,
				"/workspaces/"+args[0]+"/rebuilds/"+args[1], nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "Rebuild job '%s' has been cancelled.\n", args[1])
			return nil
		},
	}
}

// newRebuildRequeueCmd returns the 'rebuild requeue' subcommand.
// It sends POST /api/v1/workspaces/:slug/rebuilds/:id/requeue and prints
// the requeued job record.
func newRebuildRequeueCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "requeue <workspace-slug> <rebuild-id>",
		Short:         "Requeue a dead-lettered rebuild job",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost,
				"/workspaces/"+args[0]+"/rebuilds/"+args[1]+"/requeue", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newRebuildRollbackCmd returns the 'rebuild rollback' subcommand.
// It sends POST /api/v1/workspaces/:slug/rebuilds/:id/rollback and prints
// the JSON response to stdout on success, or an error to stderr on failure.
// Requirements: NS-REQ-4
func newRebuildRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "rollback <workspace-slug> <rebuild-id>",
		Short:         "Roll back the integration branch to its previous state",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost,
				"/workspaces/"+args[0]+"/rebuilds/"+args[1]+"/rollback", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}
