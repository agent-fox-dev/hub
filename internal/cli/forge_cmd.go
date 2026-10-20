package cli

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// targetUsage is the help of --target on the commands that read a workspace's forge.
const targetUsage = "Remote to read: origin (default) or upstream (carry_patch workspaces only)"

// ForgeCmd returns the 'forge' parent cobra.Command, with the subcommand that shows how
// the hub reaches the forge of a workspace.
//
// The authenticated CLI client is retrieved from the Cobra context via
// apikit.CLIClientFromCmd — credentials are resolved by apikit's PersistentPreRunE
// from flags, environment variables, and the config file.
func ForgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "forge",
		Short:         "Work with a workspace's forge",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.AddCommand(newForgeStatusCmd())
	return cmd
}

// IssueCmd returns the 'issue' parent cobra.Command, with the subcommands that read and
// write the issues of a workspace's repository on its forge.
func IssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "issue",
		Short:         "Read and write the issues of a workspace's forge",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.AddCommand(
		newIssueListCmd(), newIssueGetCmd(),
		newIssueCreateCmd(), newIssueUpdateCmd(), newIssueCloseCmd(), newIssueCommentCmd(),
		newIssueLabelCmd(), newIssueUnlabelCmd(),
	)
	return cmd
}

// PRCmd returns the 'pr' parent cobra.Command, with the subcommands that read and write the
// pull requests of a workspace's repository on its forge.
func PRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "pr",
		Short:         "Read and write the pull requests of a workspace's forge",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.AddCommand(
		newPRReadCmd("get", "Show a pull request", ""),
		newPRReadCmd("state", "Show the state of a pull request", "state"),
		newPRReadCmd("files", "List the files a pull request changes", "files"),
		newPRReadCmd("checks", "List the CI checks of a pull request", "checks"),
		newPRReadCmd("reviews", "List the reviews of a pull request", "reviews"),
		newPRCreateCmd(), newPRCommentCmd(),
	)
	return cmd
}

// newForgeStatusCmd returns the 'forge status' subcommand.
// It sends GET /api/v1/workspaces/:slug/forge and prints the response, how the hub
// reaches the forge, as one JSON document. The hub sends nothing to the forge for it.
// Requirements: 30-REQ-10.1
func newForgeStatusCmd() *cobra.Command {
	return newForgeReadCmd("status <workspace-slug>", "Show how the hub reaches the forge of a workspace", 1, false,
		func(args []string) string { return apiPath("workspaces", args[0], "forge") })
}

// newIssueListCmd returns the 'issue list' subcommand.
// It sends GET /api/v1/workspaces/:slug/issues with each flag that is set as a query
// parameter, repeated --label values joined with commas, and prints the issues. The values
// are sent as given: the hub validates them.
// Requirements: 30-REQ-10.2
func newIssueListCmd() *cobra.Command {
	var state, assignee, limit, target string
	var labels []string
	cmd := &cobra.Command{
		Use:           "list <workspace-slug>",
		Short:         "List the issues of a workspace's repository",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			query := url.Values{}
			flags := cmd.Flags()
			if flags.Changed("state") {
				query.Set("state", state)
			}
			if flags.Changed("label") {
				query.Set("labels", strings.Join(labels, ","))
			}
			if flags.Changed("assignee") {
				query.Set("assignee", assignee)
			}
			if flags.Changed("limit") {
				query.Set("limit", limit)
			}
			if flags.Changed("target") {
				query.Set("target", target)
			}
			path := apiPath("workspaces", args[0], "issues")
			if len(query) > 0 {
				path += "?" + query.Encode()
			}
			return forgeGet(cmd, path)
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "Issue state: open, closed or all (default: the hub's)")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "Only issues with this label (repeatable)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Only issues assigned to this user")
	cmd.Flags().StringVar(&limit, "limit", "", "Number of issues, 1 to 100 (default: the hub's)")
	cmd.Flags().StringVar(&target, "target", "", targetUsage)
	return cmd
}

// newIssueGetCmd returns the 'issue get' subcommand.
// It sends GET /api/v1/workspaces/:slug/issues/:n and prints the issue with its comments.
// Requirements: 30-REQ-10.3
func newIssueGetCmd() *cobra.Command {
	return newForgeReadCmd("get <workspace-slug> <number>", "Show an issue with its comments", 2, true,
		func(args []string) string { return apiPath("workspaces", args[0], "issues", args[1]) })
}

// newPRReadCmd returns a 'pr' subcommand that reads one pull request: the pull request
// itself for an empty what, else its state, files, checks or reviews.
// It sends GET /api/v1/workspaces/:slug/pulls/:n[/what].
// Requirements: 30-REQ-10.3
func newPRReadCmd(name, short, what string) *cobra.Command {
	return newForgeReadCmd(name+" <workspace-slug> <number>", short, 2, true, func(args []string) string {
		if what == "" {
			return apiPath("workspaces", args[0], "pulls", args[1])
		}
		return apiPath("workspaces", args[0], "pulls", args[1], what)
	})
}

// newForgeReadCmd returns a command that sends GET to the path route builds from the
// arguments and prints the document the hub answers. With target the command takes
// --target, which is sent when the flag is given.
func newForgeReadCmd(use, short string, nargs int, target bool, route func(args []string) string) *cobra.Command {
	var targetValue string
	cmd := &cobra.Command{
		Use:           use,
		Short:         short,
		Args:          cobra.ExactArgs(nargs),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := route(args)
			if target && cmd.Flags().Changed("target") {
				path += "?" + url.Values{"target": {targetValue}}.Encode()
			}
			return forgeGet(cmd, path)
		},
	}
	if target {
		cmd.Flags().StringVar(&targetValue, "target", "", targetUsage)
	}
	return cmd
}

// forgeGet sends GET to path and prints the document the hub answers.
func forgeGet(cmd *cobra.Command, path string) error {
	client, err := apikit.CLIClientFromCmd(cmd)
	if err != nil {
		return apikit.CLIHandleError(cmd, err)
	}
	result, err := client.DoRequest(cmd.Context(), http.MethodGet, path, nil)
	if err != nil {
		return apikit.CLIHandleError(cmd, err)
	}
	return apikit.CLIPrintResult(cmd, result)
}
