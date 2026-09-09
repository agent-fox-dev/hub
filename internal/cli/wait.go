package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// Default values for --wait flag options.
const (
	defaultWaitTimeout      = 300 * time.Second
	defaultWaitPollInterval = 5 * time.Second
)

// waitFlags holds the parsed values of the --wait, --timeout, and
// --poll-interval flags.
type waitFlags struct {
	Wait         bool
	Timeout      time.Duration
	PollInterval time.Duration
}

// addWaitFlags registers --wait, --timeout, and --poll-interval flags on
// a cobra command and returns a pointer to the parsed values. The flags
// are only effective when --wait is set.
func addWaitFlags(cmd *cobra.Command, wf *waitFlags) {
	cmd.Flags().BoolVar(&wf.Wait, "wait", false,
		"Block until the async operation reaches a terminal state")
	addPollFlags(cmd, wf)
}

// addPollFlags registers --timeout and --poll-interval for commands that
// always wait (the `wait` subcommands).
func addPollFlags(cmd *cobra.Command, wf *waitFlags) {
	cmd.Flags().DurationVar(&wf.Timeout, "timeout", defaultWaitTimeout,
		"Maximum time to wait for completion (e.g. 60s, 5m)")
	cmd.Flags().DurationVar(&wf.PollInterval, "poll-interval", defaultWaitPollInterval,
		"Interval between status polls (e.g. 5s, 10s)")
}

// isTerminalStatus returns true if the job status indicates a terminal
// state (no further transitions expected).
func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "dead_letter", "cancelled":
		return true
	default:
		return false
	}
}

// pollJobStatus polls a job status endpoint until the job reaches a
// terminal state or the timeout expires. The first poll happens
// immediately; subsequent polls every wf.PollInterval. The final job record
// is printed to stdout in every terminal state; when that state is not
// "completed" (failed, dead_letter, cancelled) the command exits non-zero so
// scripts can rely on the exit status.
//
// All errors are already reported through CLIHandleError; callers return
// them as is. statusPath is the API path for the status endpoint, e.g.
// "/workspaces/<slug>/rebuilds/<id>".
func pollJobStatus(cmd *cobra.Command, client *apikit.CLIClient, wf waitFlags, statusPath string) error {
	return pollUntil(cmd, client, wf, statusPath, "job", func(m map[string]any) (bool, string) {
		status, _ := m["status"].(string)
		if !isTerminalStatus(status) {
			return false, ""
		}
		if status == "completed" {
			return true, ""
		}
		return true, "job ended with status " + status
	})
}

// pollUntil implements the shared polling loop. done inspects a decoded
// status response and reports whether polling is finished and, if so, an
// optional failure message that turns the result into a non-zero exit.
func pollUntil(cmd *cobra.Command, client *apikit.CLIClient, wf waitFlags, statusPath, what string,
	done func(m map[string]any) (finished bool, failure string)) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), wf.Timeout)
	defer cancel()

	timedOut := func() error {
		fmt.Fprintf(cmd.ErrOrStderr(), "Timed out waiting for %s to complete after %s\n", what, wf.Timeout)
		return apikit.CLIHandleError(cmd, apikit.NewCLIError(1, fmt.Sprintf("timed out after %s", wf.Timeout)))
	}

	ticker := time.NewTicker(wf.PollInterval)
	defer ticker.Stop()

	for {
		result, err := client.DoRequest(ctx, http.MethodGet, statusPath, nil)
		if err != nil {
			// On context deadline exceeded, report timeout rather than
			// the raw HTTP error.
			if ctx.Err() != nil {
				return timedOut()
			}
			return apikit.CLIHandleError(cmd, err)
		}

		if m, ok := result.(map[string]any); ok {
			finished, failure := done(m)
			if finished {
				if err := apikit.CLIPrintResult(cmd, result); err != nil {
					return err
				}
				if failure != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), strings.ToUpper(failure[:1])+failure[1:]+".")
					return apikit.CLIHandleError(cmd, apikit.NewCLIError(1, failure))
				}
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return timedOut()
		case <-ticker.C:
		}
	}
}

// extractJobID extracts the "id" field from an API response. Returns the
// id string or an error if the field is missing or not a string.
func extractJobID(result any) (string, error) {
	m, ok := result.(map[string]any)
	if !ok {
		return "", fmt.Errorf("unexpected response format")
	}
	id, ok := m["id"].(string)
	if !ok || id == "" {
		return "", fmt.Errorf("response missing 'id' field")
	}
	return id, nil
}

// extractRebuildJobID extracts the "rebuild_job_id" field from a sync
// response. Returns empty string if the field is absent (no rebuild
// triggered).
func extractRebuildJobID(result any) string {
	m, ok := result.(map[string]any)
	if !ok {
		return ""
	}
	id, _ := m["rebuild_job_id"].(string)
	return id
}

// extractCloneStatus extracts the "clone_status" field from a workspace
// response.
func extractCloneStatus(result any) string {
	m, ok := result.(map[string]any)
	if !ok {
		return ""
	}
	status, _ := m["clone_status"].(string)
	return status
}

// pollWorkspaceCloneStatus polls a workspace's clone_status until it
// reaches "ready" or "failed", or the timeout expires. A failed clone
// exits non-zero after printing the workspace record.
func pollWorkspaceCloneStatus(cmd *cobra.Command, client *apikit.CLIClient, wf waitFlags, slug string) error {
	return pollUntil(cmd, client, wf, apiPath("workspaces", slug), "workspace clone", func(m map[string]any) (bool, string) {
		switch extractCloneStatus(m) {
		case "ready":
			return true, ""
		case "failed":
			return true, "workspace clone failed"
		default:
			return false, ""
		}
	})
}

// printJSON marshals v to JSON and writes it to cmd's output writer.
// This is used to print intermediate results (e.g. the initial submit
// response) before polling begins.
func printJSON(cmd *cobra.Command, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
}
