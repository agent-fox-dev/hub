package cli

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// newForgeWebhookCmd returns the 'forge webhook' parent with the subcommands that set and remove
// the secret a workspace's forge signs its deliveries with.
func newForgeWebhookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "webhook",
		Short:         "Set or remove the webhook secret of a workspace's forge",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.AddCommand(newWebhookSetCmd(), newWebhookUnsetCmd())
	return cmd
}

// newWebhookSetCmd returns the 'forge webhook set' subcommand.
// It sends PUT /api/v1/workspaces/:slug/forge/webhook with the secret of --secret or --from-stdin,
// or an empty object for --generate and for none of them, and prints the hub's answer: the URL to
// give the forge and, when the hub made the secret up, the secret. That is the one time the hub
// shows it, so a line on stderr says so.
// Requirements: 32-REQ-10.1, 32-REQ-10.2, 32-REQ-10.3, 32-REQ-10.4
func newWebhookSetCmd() *cobra.Command {
	var secret string
	var fromStdin, generate bool
	cmd := &cobra.Command{
		Use:           "set <workspace-slug> [--secret s | --from-stdin | --generate]",
		Short:         "Set the webhook secret of a workspace's forge and show the URL for the forge's settings",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			chosen := 0
			for _, name := range []string{"secret", "from-stdin", "generate"} {
				if flags.Changed(name) {
					chosen++
				}
			}
			if chosen > 1 {
				return usageError(cmd, "--secret, --from-stdin and --generate are mutually exclusive")
			}
			payload := map[string]any{}
			switch {
			case flags.Changed("secret"):
				payload["secret"] = secret
			case flags.Changed("from-stdin"):
				value, err := readValueFromStdin(cmd)
				if err != nil {
					return apikit.CLIHandleError(cmd, err)
				}
				payload["secret"] = value
			}

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}
			result, err := client.DoRequest(cmd.Context(), http.MethodPut, apiPath("workspaces", args[0], "forge", "webhook"), payload)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}
			if doc, ok := result.(map[string]any); ok && doc["secret"] != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: hub will not show this secret again; enter it in the webhook settings of the forge now")
			}
			return apikit.CLIPrintResult(cmd, result)
		},
	}
	cmd.Flags().StringVar(&secret, "secret", "", "The secret to set, 16 to 255 printable ASCII characters (it ends up in the shell history: prefer --from-stdin)")
	cmd.Flags().BoolVar(&fromStdin, "from-stdin", false, "Read the secret from stdin; one trailing newline is dropped")
	cmd.Flags().BoolVar(&generate, "generate", false, "Let the hub make up a secret (the default)")
	return cmd
}

// newWebhookUnsetCmd returns the 'forge webhook unset' subcommand.
// It sends DELETE /api/v1/workspaces/:slug/forge/webhook, after which the hub refuses the
// forge's deliveries, and prints {"status":"unset","workspace":...}.
// Requirements: 32-REQ-10.5
func newWebhookUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "unset <workspace-slug>",
		Short:         "Remove the webhook secret of a workspace's forge, so that the hub refuses its deliveries",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := apiPath("workspaces", args[0], "forge", "webhook")
			return forgeSend(cmd, http.MethodDelete, path, nil, func() any {
				return map[string]any{"status": "unset", "workspace": args[0]}
			})
		},
	}
}

// newForgeDeliveriesCmd returns the 'forge deliveries' subcommand.
// It sends GET /api/v1/workspaces/:slug/forge/deliveries with --since and --limit when they are
// set, as given: the hub validates them. It prints the log of the forge's deliveries, newest
// first.
// Requirements: 32-REQ-10.6
func newForgeDeliveriesCmd() *cobra.Command {
	var since, limit string
	cmd := &cobra.Command{
		Use:           "deliveries <workspace-slug>",
		Short:         "List the deliveries the hub received from a workspace's forge, newest first",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			query := url.Values{}
			if cmd.Flags().Changed("since") {
				query.Set("since", since)
			}
			if cmd.Flags().Changed("limit") {
				query.Set("limit", limit)
			}
			path := apiPath("workspaces", args[0], "forge", "deliveries")
			if len(query) > 0 {
				path += "?" + query.Encode()
			}
			return forgeGet(cmd, path)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "Only deliveries received at this RFC 3339 time or later (default: the hub's)")
	cmd.Flags().StringVar(&limit, "limit", "", "Number of deliveries, 1 to 100 (default: the hub's)")
	return cmd
}
