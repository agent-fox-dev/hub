package cli

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// SandboxCmd returns the 'sandbox' parent cobra.Command with the subcommands
// that list the deployment's sandbox profiles and that issue, list, show and
// end sandbox leases.
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

	cmd.AddCommand(
		newSandboxProfilesCmd(),
		newSandboxCreateCmd(),
		newSandboxListCmd(),
		newSandboxGetCmd(),
		newSandboxDestroyCmd(),
	)

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

// writeTokenFile writes the token to the file opened for --token-file. It is a
// variable so that a test can make the write fail.
var writeTokenFile = func(f *os.File, token string) error {
	_, err := f.WriteString(token)
	return err
}

// newSandboxCreateCmd returns the 'sandbox create' subcommand.
// It sends POST /api/v1/workspaces/:slug/sandboxes and prints the lease. The
// response carries the lease token, which hub shows once: without --token-file
// it is printed with the lease and stderr says so; with --token-file it is
// written to the file with mode 0600 and kept out of stdout.
// Requirements: 26-REQ-10
func newSandboxCreateCmd() *cobra.Command {
	var profile, purpose, ttl, tokenFile string
	var labels []string
	cmd := &cobra.Command{
		Use:           "create <workspace-slug>",
		Short:         "Issue a sandbox lease in a workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]
			labelMap, err := parseLabelFlags(labels)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}
			body := map[string]any{}
			for name, value := range map[string]string{"profile": profile, "purpose": purpose, "ttl": ttl} {
				if cmd.Flags().Changed(name) {
					body[name] = value // ttl goes to the server unparsed
				}
			}
			if len(labelMap) > 0 {
				body["labels"] = labelMap
			}

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			// The file is opened before the request, so that no lease is issued
			// whose token has nowhere to go.
			var out *os.File
			if tokenFile != "" {
				if out, err = openTokenFile(tokenFile); err != nil {
					return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, fmt.Sprintf("cannot open the token file %s: %v", tokenFile, err)))
				}
				defer out.Close()
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost, apiPath("workspaces", slug, "sandboxes"), body)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}
			if out == nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: the token in this output will not be shown again; save it now")
				return apikit.CLIPrintResult(cmd, result)
			}

			lease, _ := result.(map[string]any)
			token, _ := lease["token"].(string)
			delete(lease, "token")
			if err := storeToken(out, token); err != nil {
				// No lease is left whose token the caller has lost, and the token
				// never falls back to stdout.
				id, _ := lease["id"].(string)
				if id != "" {
					_, _ = client.DoRequest(cmd.Context(), http.MethodDelete, apiPath("workspaces", slug, "sandboxes", id), nil)
				}
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(1, fmt.Sprintf("cannot write the token to %s: %v; the lease %s was released", tokenFile, err, id)))
			}
			return apikit.CLIPrintResult(cmd, lease)
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "", "Profile name (default: the hub's default profile)")
	cmd.Flags().StringVar(&purpose, "purpose", "", "What the sandbox is for (at most 64 characters)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "Lifetime of the lease, such as 30m (default: the profile's)")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "Label as key=value (repeatable)")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "Write the lease token to this file (mode 0600) instead of printing it")
	return cmd
}

// parseLabelFlags turns repeated --label key=value flags into a map. A label
// without '=' or a key given twice is a usage error (exit code 2).
func parseLabelFlags(flags []string) (map[string]string, error) {
	labels := make(map[string]string, len(flags))
	for _, flag := range flags {
		key, value, err := parseKeyValue(flag)
		if err != nil {
			return nil, err
		}
		if _, dup := labels[key]; dup {
			return nil, apikit.NewCLIError(2, fmt.Sprintf("label %q is given more than once", key))
		}
		labels[key] = value
	}
	return labels, nil
}

// openTokenFile creates or truncates the file and sets its mode to 0600 with an
// explicit chmod, which the umask does not narrow.
func openTokenFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// storeToken writes the token, with no trailing newline, and closes the file.
func storeToken(f *os.File, token string) error {
	if token == "" {
		f.Close()
		return fmt.Errorf("the hub returned no token")
	}
	if err := writeTokenFile(f, token); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// newSandboxListCmd returns the 'sandbox list' subcommand.
// It sends GET /api/v1/workspaces/:slug/sandboxes, with ?all=true for --all,
// and prints the leases.
func newSandboxListCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:           "list <workspace-slug>",
		Short:         "List the sandbox leases of a workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}
			path := apiPath("workspaces", args[0], "sandboxes")
			if all {
				path += "?all=true"
			}
			result, err := client.DoRequest(cmd.Context(), http.MethodGet, path, nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}
			return apikit.CLIPrintResult(cmd, result)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include leases that have ended")
	return cmd
}

// newSandboxGetCmd returns the 'sandbox get' subcommand.
// It sends GET /api/v1/workspaces/:slug/sandboxes/:id and prints the lease.
func newSandboxGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "get <workspace-slug> <lease-id>",
		Short:         "Show one sandbox lease",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}
			result, err := client.DoRequest(cmd.Context(), http.MethodGet, apiPath("workspaces", args[0], "sandboxes", args[1]), nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}
			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newSandboxDestroyCmd returns the 'sandbox destroy' subcommand.
// It sends DELETE /api/v1/workspaces/:slug/sandboxes/:id. The hub answers 204
// with no body, so the command prints a synthetic status document to stdout and
// a confirmation to stderr.
func newSandboxDestroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "destroy <workspace-slug> <lease-id>",
		Short:         "End a sandbox lease and revoke its token",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}
			id := args[1]
			if _, err := client.DoRequest(cmd.Context(), http.MethodDelete, apiPath("workspaces", args[0], "sandboxes", id), nil); err != nil {
				return apikit.CLIHandleError(cmd, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Sandbox lease %s destroyed.\n", id)
			return apikit.CLIPrintResult(cmd, map[string]string{"status": "destroyed", "id": id})
		},
	}
}
