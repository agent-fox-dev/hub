package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// targetWriteUsage is the help of --target on the commands that write to a workspace's forge.
const targetWriteUsage = "Remote to write to: origin (default) or upstream (carry_patch workspaces only)"

// usageError returns a usage error, exit code 2, for a mistake the command can see before it
// sends anything.
func usageError(cmd *cobra.Command, format string, args ...any) error {
	return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, fmt.Sprintf(format, args...)))
}

// positiveNumber reads the number of an issue or pull request. The writes check it here, so
// that a mistyped number does not reach the hub.
func positiveNumber(arg string) (string, bool) {
	n, err := strconv.Atoi(arg)
	if err != nil || n <= 0 {
		return "", false
	}
	return strconv.Itoa(n), true
}

// bodyFlags are --body and --body-file of a command that takes a text.
type bodyFlags struct {
	body, file string
}

// add defines the flags; what names the text in their help.
func (f *bodyFlags) add(cmd *cobra.Command, what string) {
	cmd.Flags().StringVar(&f.body, "body", "", what)
	cmd.Flags().StringVar(&f.file, "body-file", "", "Read the text from this file, or from stdin for -; it is sent as it is, with no trailing newline removed")
}

// set reports whether either flag was given.
func (f *bodyFlags) set(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("body") || cmd.Flags().Changed("body-file")
}

// text returns the text of the flags. A file or stdin is read as it is: unlike a secret, a
// body is a document, and a trailing newline is part of it. Giving both flags, or a file
// that cannot be read, is a usage error.
func (f *bodyFlags) text(cmd *cobra.Command) (string, error) {
	if cmd.Flags().Changed("body") && cmd.Flags().Changed("body-file") {
		return "", fmt.Errorf("--body and --body-file cannot be used together")
	}
	if !cmd.Flags().Changed("body-file") {
		return f.body, nil
	}
	var data []byte
	var err error
	if f.file == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(f.file)
	}
	if err != nil {
		return "", fmt.Errorf("cannot read the body from %s: %v", f.file, err)
	}
	return string(data), nil
}

// withTarget adds ?target= to path when --target was given.
func withTarget(cmd *cobra.Command, path, target string) string {
	if cmd.Flags().Changed("target") {
		return path + "?" + url.Values{"target": {target}}.Encode()
	}
	return path
}

// forgeSend sends one write to the hub and prints the document it answers, one JSON
// document, or what print makes of an answer that has none.
func forgeSend(cmd *cobra.Command, method, path string, body any, print func() any) error {
	client, err := apikit.CLIClientFromCmd(cmd)
	if err != nil {
		return apikit.CLIHandleError(cmd, err)
	}
	result, err := client.DoRequest(cmd.Context(), method, path, body)
	if err != nil {
		return apikit.CLIHandleError(cmd, err)
	}
	if print != nil {
		result = print()
	}
	return apikit.CLIPrintResult(cmd, result)
}

// newIssueCreateCmd returns the 'issue create' subcommand.
// It sends POST /api/v1/workspaces/:slug/issues with the flags that are set and prints the
// issue the hub answers.
// Requirements: 31-REQ-10.1, 31-REQ-10.3, 31-REQ-10.4, 31-REQ-10.5
func newIssueCreateCmd() *cobra.Command {
	var title, target string
	var labels, assignees []string
	var body bodyFlags
	cmd := &cobra.Command{
		Use:           "create <workspace-slug>",
		Short:         "Create an issue on a workspace's forge",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return usageError(cmd, "--title flag is required")
			}
			payload := map[string]any{"title": title}
			if body.set(cmd) {
				text, err := body.text(cmd)
				if err != nil {
					return usageError(cmd, "%v", err)
				}
				payload["body"] = text
			}
			if cmd.Flags().Changed("label") {
				payload["labels"] = labels
			}
			if cmd.Flags().Changed("assignee") {
				payload["assignees"] = assignees
			}
			return forgeSend(cmd, http.MethodPost, withTarget(cmd, apiPath("workspaces", args[0], "issues"), target), payload, nil)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "Title of the issue (required)")
	body.add(cmd, "Body of the issue")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "Label to put on the issue (repeatable)")
	cmd.Flags().StringArrayVar(&assignees, "assignee", nil, "User to assign the issue to (repeatable)")
	cmd.Flags().StringVar(&target, "target", "", targetWriteUsage)
	return cmd
}

// newIssueUpdateCmd returns the 'issue update' subcommand.
// It sends PATCH /api/v1/workspaces/:slug/issues/:n with only the flags that are set, so
// that --body "" clears the body, and prints the issue the hub answers.
// Requirements: 31-REQ-10.1, 31-REQ-10.3, 31-REQ-10.4, 31-REQ-10.5
func newIssueUpdateCmd() *cobra.Command {
	var title, state, target string
	var labels, assignees []string
	var body bodyFlags
	cmd := &cobra.Command{
		Use:           "update <workspace-slug> <number>",
		Short:         "Change an issue on a workspace's forge",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, ok := positiveNumber(args[1])
			if !ok {
				return usageError(cmd, "the issue number must be a positive integer")
			}
			payload := map[string]any{}
			if cmd.Flags().Changed("title") {
				payload["title"] = title
			}
			if cmd.Flags().Changed("state") {
				payload["state"] = state
			}
			if body.set(cmd) {
				text, err := body.text(cmd)
				if err != nil {
					return usageError(cmd, "%v", err)
				}
				payload["body"] = text
			}
			if cmd.Flags().Changed("label") {
				payload["labels"] = labels
			}
			if cmd.Flags().Changed("assignee") {
				payload["assignees"] = assignees
			}
			if len(payload) == 0 {
				return usageError(cmd, "nothing to update: set at least one of --title, --body, --body-file, --state, --label and --assignee")
			}
			return forgeSend(cmd, http.MethodPatch, withTarget(cmd, apiPath("workspaces", args[0], "issues", n), target), payload, nil)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "New title")
	body.add(cmd, "New body; an empty --body clears it")
	cmd.Flags().StringVar(&state, "state", "", "New state: open or closed")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "Label to set (repeatable); the labels given replace the issue's whole set")
	cmd.Flags().StringArrayVar(&assignees, "assignee", nil, "User to assign (repeatable); the users given replace the issue's whole set")
	cmd.Flags().StringVar(&target, "target", "", targetWriteUsage)
	return cmd
}

// newIssueCloseCmd returns the 'issue close' subcommand.
// It sends POST /api/v1/workspaces/:slug/issues/:n/close, with {"comment": ...} when
// --comment is given, and prints the closed issue the hub answers.
// Requirements: 31-REQ-10.1, 31-REQ-10.4, 31-REQ-10.5
func newIssueCloseCmd() *cobra.Command {
	var comment, target string
	cmd := &cobra.Command{
		Use:           "close <workspace-slug> <number>",
		Short:         "Close an issue on a workspace's forge, with a comment if given",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, ok := positiveNumber(args[1])
			if !ok {
				return usageError(cmd, "the issue number must be a positive integer")
			}
			var payload any
			if cmd.Flags().Changed("comment") {
				payload = map[string]any{"comment": comment}
			}
			return forgeSend(cmd, http.MethodPost, withTarget(cmd, apiPath("workspaces", args[0], "issues", n, "close"), target), payload, nil)
		},
	}
	cmd.Flags().StringVar(&comment, "comment", "", "Comment to post on the issue before it is closed")
	cmd.Flags().StringVar(&target, "target", "", targetWriteUsage)
	return cmd
}

// newIssueCommentCmd returns the 'issue comment' subcommand.
// It sends POST /api/v1/workspaces/:slug/issues/:n/comments and prints {"url": ...}.
// Requirements: 31-REQ-10.1, 31-REQ-10.3, 31-REQ-10.4
func newIssueCommentCmd() *cobra.Command {
	return newCommentCmd("issue", "issues", "Comment on an issue on a workspace's forge")
}

// newCommentCmd returns a 'comment' subcommand for issues or pull requests; kind names
// what is commented on and collection is its route.
func newCommentCmd(kind, collection, short string) *cobra.Command {
	var target string
	var body bodyFlags
	cmd := &cobra.Command{
		Use:           "comment <workspace-slug> <number>",
		Short:         short,
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, ok := positiveNumber(args[1])
			if !ok {
				return usageError(cmd, "the %s number must be a positive integer", kind)
			}
			if !body.set(cmd) {
				return usageError(cmd, "--body or --body-file flag is required")
			}
			text, err := body.text(cmd)
			if err != nil {
				return usageError(cmd, "%v", err)
			}
			path := withTarget(cmd, apiPath("workspaces", args[0], collection, n, "comments"), target)
			return forgeSend(cmd, http.MethodPost, path, map[string]any{"body": text}, nil)
		},
	}
	body.add(cmd, "Text of the comment")
	cmd.Flags().StringVar(&target, "target", "", targetWriteUsage)
	return cmd
}

// labeledIssue is what 'issue label' prints when the hub answers 204.
type labeledIssue struct {
	Status string   `json:"status"`
	Number int      `json:"number"`
	Labels []string `json:"labels"`
}

// unlabeledIssue is what 'issue unlabel' prints when the hub answers 204.
type unlabeledIssue struct {
	Status string `json:"status"`
	Number int    `json:"number"`
	Label  string `json:"label"`
}

// newIssueLabelCmd returns the 'issue label' subcommand.
// It sends POST /api/v1/workspaces/:slug/issues/:n/labels with the labels as a bare JSON
// array. The hub answers 204, so the command prints a status document.
// Requirements: 31-REQ-10.2, 31-REQ-10.4
func newIssueLabelCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:           "label <workspace-slug> <number> <label>...",
		Short:         "Add labels to an issue on a workspace's forge",
		Args:          cobra.MinimumNArgs(3),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, ok := positiveNumber(args[1])
			if !ok {
				return usageError(cmd, "the issue number must be a positive integer")
			}
			labels := args[2:]
			number, _ := strconv.Atoi(n)
			return forgeSend(cmd, http.MethodPost, withTarget(cmd, apiPath("workspaces", args[0], "issues", n, "labels"), target), labels,
				func() any { return labeledIssue{Status: "labeled", Number: number, Labels: labels} })
		},
	}
	cmd.Flags().StringVar(&target, "target", "", targetWriteUsage)
	return cmd
}

// newIssueUnlabelCmd returns the 'issue unlabel' subcommand.
// It sends DELETE /api/v1/workspaces/:slug/issues/:n/labels/:label, the label
// percent-encoded as a path segment. The hub answers 204, so the command prints a status
// document.
// Requirements: 31-REQ-10.2, 31-REQ-10.4
func newIssueUnlabelCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:           "unlabel <workspace-slug> <number> <label>",
		Short:         "Remove a label from an issue on a workspace's forge",
		Args:          cobra.ExactArgs(3),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, ok := positiveNumber(args[1])
			if !ok {
				return usageError(cmd, "the issue number must be a positive integer")
			}
			number, _ := strconv.Atoi(n)
			path := withTarget(cmd, apiPath("workspaces", args[0], "issues", n, "labels", args[2]), target)
			return forgeSend(cmd, http.MethodDelete, path, nil,
				func() any { return unlabeledIssue{Status: "unlabeled", Number: number, Label: args[2]} })
		},
	}
	cmd.Flags().StringVar(&target, "target", "", targetWriteUsage)
	return cmd
}

// newPRCreateCmd returns the 'pr create' subcommand.
// It sends POST /api/v1/workspaces/:slug/pulls and prints the pull request the hub answers.
// The head is sent as given, so that owner:branch opens a pull request from a fork.
// Requirements: 31-REQ-10.1, 31-REQ-10.3, 31-REQ-10.4, 31-REQ-10.5
func newPRCreateCmd() *cobra.Command {
	var title, head, base, target string
	var draft bool
	var body bodyFlags
	cmd := &cobra.Command{
		Use:           "create <workspace-slug>",
		Short:         "Open a pull request on a workspace's forge",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, required := range []struct{ name, value string }{{"title", title}, {"head", head}, {"base", base}} {
				if required.value == "" {
					return usageError(cmd, "--%s flag is required", required.name)
				}
			}
			payload := map[string]any{"title": title, "head": head, "base": base}
			if body.set(cmd) {
				text, err := body.text(cmd)
				if err != nil {
					return usageError(cmd, "%v", err)
				}
				payload["body"] = text
			}
			if cmd.Flags().Changed("draft") {
				payload["draft"] = draft
			}
			return forgeSend(cmd, http.MethodPost, withTarget(cmd, apiPath("workspaces", args[0], "pulls"), target), payload, nil)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "Title of the pull request (required)")
	cmd.Flags().StringVar(&head, "head", "", "Branch with the changes; owner:branch for a fork (required)")
	cmd.Flags().StringVar(&base, "base", "", "Branch to merge into (required)")
	body.add(cmd, "Body of the pull request")
	cmd.Flags().BoolVar(&draft, "draft", false, "Open the pull request as a draft")
	cmd.Flags().StringVar(&target, "target", "", targetWriteUsage)
	return cmd
}

// newPRCommentCmd returns the 'pr comment' subcommand.
// It sends POST /api/v1/workspaces/:slug/pulls/:n/comments and prints {"url": ...}.
// Requirements: 31-REQ-10.1, 31-REQ-10.3, 31-REQ-10.4
func newPRCommentCmd() *cobra.Command {
	return newCommentCmd("pull request", "pulls", "Comment on a pull request on a workspace's forge")
}
