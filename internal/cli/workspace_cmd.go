package cli

import (
	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// WorkspaceCmd returns the 'workspace' parent cobra.Command with subcommands
// for create, list, get, archive, reactivate, and delete.
//
// baseURL is the hub API base URL (e.g. "http://localhost:8080").
// apiKey is the authentication credential for API calls.
func WorkspaceCmd(baseURL, apiKey string) *cobra.Command {
	panic("not implemented")
}

// BuildRootCommand creates the full afc CLI command tree by calling
// apikit.RootCommand() and adding workspace subcommands alongside the
// standard apikit commands (login, user, keys, tokens, orgs, admin).
func BuildRootCommand() *cobra.Command {
	panic("not implemented")
}

// Ensure apikit is used (for go vet / import validation).
var _ = apikit.RootCommand
