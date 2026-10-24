package cli

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// TokensCmd returns apikit's 'tokens' command with --workspace added to its
// create and list subcommands. Without the flag both behave
// exactly as apikit's do; with it, create mints a token bound to the
// workspace (POST /workspaces/<slug>/tokens) and list lists the workspace's
// bound tokens (GET /workspaces/<slug>/tokens).
func TokensCmd() *cobra.Command {
	cmd, err := wrapTokensCmd(apikit.TokensCmd())
	if err != nil {
		// apikit's command tree changed under hub; the unit test of
		// wrapTokensCmd catches this before a release.
		panic(err)
	}
	return cmd
}

// wrapTokensCmd adds --workspace to the create and list subcommands of
// apikit's tokens command and wraps their RunE (or Run). It fails when either
// subcommand is missing.
func wrapTokensCmd(cmd *cobra.Command) (*cobra.Command, error) {
	sub := map[string]*cobra.Command{}
	for _, c := range cmd.Commands() {
		sub[c.Name()] = c
	}
	for _, name := range []string{"create", "list"} {
		if sub[name] == nil {
			return nil, fmt.Errorf("apikit's tokens command has no %q subcommand", name)
		}
	}
	wrapWithWorkspace(sub["create"], "Bind the token to this workspace (POST /workspaces/<slug>/tokens)", runCreateBound)
	wrapWithWorkspace(sub["list"], "List the tokens bound to this workspace (GET /workspaces/<slug>/tokens)", runListBound)
	return cmd, nil
}

// wrapWithWorkspace adds --workspace to cmd and runs bound when the flag is
// given, apikit's own behaviour otherwise.
func wrapWithWorkspace(cmd *cobra.Command, usage string, bound func(cmd *cobra.Command, slug string) error) {
	cmd.Flags().String("workspace", "", usage)
	original := cmd.RunE
	if original == nil {
		run := cmd.Run
		original = func(c *cobra.Command, args []string) error {
			if run != nil {
				run(c, args)
			}
			return nil
		}
	}
	cmd.Run = nil
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if !c.Flags().Changed("workspace") {
			return original(c, args)
		}
		slug, _ := c.Flags().GetString("workspace")
		if strings.TrimSpace(slug) == "" {
			return apikit.CLIHandleError(c, apikit.NewCLIError(2, "--workspace must not be empty"))
		}
		return bound(c, slug)
	}
}

// runCreateBound mints a token bound to slug, prints the response once and
// warns on stderr that the token will not be shown again.
func runCreateBound(cmd *cobra.Command, slug string) error {
	name, _ := cmd.Flags().GetString("name")
	if strings.TrimSpace(name) == "" {
		return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "--name must not be empty"))
	}
	raw, _ := cmd.Flags().GetString("permissions")
	var permissions []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			permissions = append(permissions, p)
		}
	}
	if len(permissions) == 0 {
		return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "--permissions must name at least one permission"))
	}
	expires, _ := cmd.Flags().GetInt("expires")

	client, err := apikit.CLIClientFromCmd(cmd)
	if err != nil {
		return apikit.CLIHandleError(cmd, err)
	}
	result, err := client.DoRequest(cmd.Context(), http.MethodPost, apiPath("workspaces", slug, "tokens"), map[string]any{
		"name":        name,
		"permissions": permissions,
		"expires":     expires,
	})
	if err != nil {
		return apikit.CLIHandleError(cmd, err)
	}
	if err := apikit.CLIPrintResult(cmd, result); err != nil {
		return err
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "warning: the token in this output will not be shown again; save it now")
	return nil
}

// runListBound prints the tokens bound to slug.
func runListBound(cmd *cobra.Command, slug string) error {
	client, err := apikit.CLIClientFromCmd(cmd)
	if err != nil {
		return apikit.CLIHandleError(cmd, err)
	}
	result, err := client.DoRequest(cmd.Context(), http.MethodGet, apiPath("workspaces", slug, "tokens"), nil)
	if err != nil {
		return apikit.CLIHandleError(cmd, err)
	}
	return apikit.CLIPrintResult(cmd, result)
}
