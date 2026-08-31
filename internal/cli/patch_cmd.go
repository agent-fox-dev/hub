package cli

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// PatchCmd returns the 'patch' parent cobra.Command with subcommands
// for add, list, remove, reorder, and update.
//
// Requirements: 15-REQ-14
func PatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "patch",
		Short:         "Manage patches",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newPatchAddCmd(),
		newPatchListCmd(),
		newPatchRemoveCmd(),
		newPatchReorderCmd(),
		newPatchUpdateCmd(),
		newPatchRestoreCmd(),
	)

	return cmd
}

// newPatchAddCmd returns the 'patch add' subcommand.
// It sends POST /api/v1/workspaces/:slug/patches with the provided branch name
// and optional position, upstream-pr, and description flags.
// Requirements: 15-REQ-14.1
func newPatchAddCmd() *cobra.Command {
	var (
		branch          string
		position        int
		upstreamPR      string
		description     string
		skipBranchCheck bool
		ifNotExists     bool
	)

	cmd := &cobra.Command{
		Use:           "add <workspace-slug>",
		Short:         "Add a patch to a workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 15-REQ-14.E1: --branch is required.
			if branch == "" {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "--branch flag is required"))
			}

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			body := map[string]any{
				"branch_name": branch,
			}
			if cmd.Flags().Changed("position") {
				body["position"] = position
			}
			if upstreamPR != "" {
				body["upstream_pr_url"] = upstreamPR
			}
			if description != "" {
				body["description"] = description
			}
			if skipBranchCheck {
				body["skip_branch_check"] = true
			}
			if ifNotExists {
				body["if_not_exists"] = true
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost, "/workspaces/"+args[0]+"/patches", body)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}

	cmd.Flags().StringVar(&branch, "branch", "", "Branch name for the patch (required)")
	cmd.Flags().IntVar(&position, "position", 0, "Position in the patch list (optional)")
	cmd.Flags().StringVar(&upstreamPR, "upstream-pr", "", "Upstream PR URL (optional)")
	cmd.Flags().StringVar(&description, "description", "", "Patch description (optional)")
	cmd.Flags().BoolVar(&skipBranchCheck, "skip-branch-check", false, "Skip branch existence validation (optional)")
	cmd.Flags().BoolVar(&ifNotExists, "if-not-exists", false, "Return existing patch instead of error if branch already registered (optional)")

	return cmd
}

// newPatchListCmd returns the 'patch list' subcommand.
// It sends GET /api/v1/workspaces/:slug/patches and prints the patch list
// in position order.
// Requirements: 15-REQ-14.2
func newPatchListCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "list <workspace-slug>",
		Short:         "List patches for a workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet, "/workspaces/"+args[0]+"/patches", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newPatchRemoveCmd returns the 'patch remove' subcommand.
// It sends DELETE /api/v1/workspaces/:slug/patches/:id and prints confirmation.
// Requirements: 15-REQ-14.3
func newPatchRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "remove <workspace-slug> <patch-id>",
		Short:         "Remove a patch from a workspace",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			_, err = client.DoRequest(cmd.Context(), http.MethodDelete, "/workspaces/"+args[0]+"/patches/"+args[1], nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			// Emit synthetic JSON status object to stdout (server returns 204 No Content).
			synthetic := map[string]string{
				"status":   "removed",
				"patch_id": args[1],
			}
			data, _ := json.MarshalIndent(synthetic, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(data))

			fmt.Fprintf(cmd.ErrOrStderr(), "Patch '%s' has been removed.\n", args[1])
			return nil
		},
	}
}

// newPatchReorderCmd returns the 'patch reorder' subcommand.
// It sends POST /api/v1/workspaces/:slug/patches/reorder with the provided
// patch IDs in the given order and prints the reordered list.
// Requirements: 15-REQ-14.4
func newPatchReorderCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "reorder <workspace-slug> <patch-id-1> [patch-id-2] ...",
		Short:         "Reorder patches for a workspace",
		Args:          cobra.MinimumNArgs(2), // workspace-slug + at least one patch ID
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			body := map[string]any{
				"patch_ids": args[1:],
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost, "/workspaces/"+args[0]+"/patches/reorder", body)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newPatchUpdateCmd returns the 'patch update' subcommand.
// It sends PATCH /api/v1/workspaces/:slug/patches/:id with the provided fields
// and prints the updated patch.
// Requirements: 15-REQ-14.5
func newPatchUpdateCmd() *cobra.Command {
	var (
		status      string
		description string
		upstreamPR  string
		position    int
	)

	cmd := &cobra.Command{
		Use:           "update <workspace-slug> <patch-id>",
		Short:         "Update a patch",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			body := map[string]any{}
			if cmd.Flags().Changed("status") {
				body["status"] = status
			}
			if cmd.Flags().Changed("description") {
				body["description"] = description
			}
			if cmd.Flags().Changed("upstream-pr") {
				body["upstream_pr_url"] = upstreamPR
			}
			if cmd.Flags().Changed("position") {
				body["position"] = position
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPatch, "/workspaces/"+args[0]+"/patches/"+args[1], body)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Patch status (active, merged_upstream, conflict, disabled)")
	cmd.Flags().StringVar(&description, "description", "", "Patch description")
	cmd.Flags().StringVar(&upstreamPR, "upstream-pr", "", "Upstream PR URL")
	cmd.Flags().IntVar(&position, "position", 0, "New position")

	return cmd
}

// newPatchRestoreCmd returns the 'patch restore' subcommand.
// It sends POST /api/v1/workspaces/:slug/patches/:id/restore to transition
// a soft-deleted patch back to active status.
func newPatchRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "restore <workspace-slug> <patch-id>",
		Short:         "Restore a soft-deleted patch",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost, "/workspaces/"+args[0]+"/patches/"+args[1]+"/restore", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}
