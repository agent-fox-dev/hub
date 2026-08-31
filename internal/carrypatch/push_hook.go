package carrypatch

import (
	"database/sql"
	"encoding/json"
	"log"

	"github.com/google/uuid"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// NewPostPushRebuildHook returns a function suitable for use with
// gitserver.RegisterPostPushHook. It checks whether any pushed branch is
// a registered patch in a carry_patch workspace and, if AUTO_REBUILD_AFTER_PUSH
// is not "false", enqueues a rebuild job with duplicate suppression.
//
// The hook is called asynchronously after a successful push; errors are logged
// but do not affect the push response.
func NewPostPushRebuildHook(
	queue *jobqueue.Queue,
	getVariable GetVariableFunc,
) func(db *sql.DB, slug string, branches []string) {
	return func(db *sql.DB, slug string, branches []string) {
		postPushRebuildHook(db, slug, branches, queue, getVariable)
	}
}

// postPushRebuildHook implements the post-push auto-rebuild logic.
//
// Steps:
//  1. Load workspace mode — skip if not carry_patch (NS-REQ-5).
//  2. Check if any pushed branch matches a registered patch (NS-REQ-3).
//  3. Check AUTO_REBUILD_AFTER_PUSH variable — skip if "false" (NS-REQ-2).
//  4. Enqueue rebuild job with duplicate suppression (NS-REQ-1, NS-REQ-4).
func postPushRebuildHook(
	db *sql.DB,
	slug string,
	branches []string,
	queue *jobqueue.Queue,
	getVariable GetVariableFunc,
) {
	// 1. Load workspace mode and integration_branch.
	var workspaceMode string
	var integrationBranch sql.NullString
	err := db.QueryRow(
		`SELECT workspace_mode, integration_branch FROM workspaces WHERE slug = ?`,
		slug,
	).Scan(&workspaceMode, &integrationBranch)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Printf("post-push hook: failed to query workspace %q: %v", slug, err)
		}
		return
	}

	// NS-REQ-5: standard workspaces never trigger a rebuild.
	if workspaceMode != "carry_patch" {
		return
	}

	// 2. Check if any pushed branch is a registered patch (NS-REQ-3).
	matched := false
	for _, branch := range branches {
		var count int
		err := db.QueryRow(
			`SELECT COUNT(*) FROM patches WHERE workspace_slug = ? AND branch_name = ?`,
			slug, branch,
		).Scan(&count)
		if err != nil {
			log.Printf("post-push hook: failed to check patch for branch %q in %q: %v", branch, slug, err)
			continue
		}
		if count > 0 {
			matched = true
			break
		}
	}
	if !matched {
		return
	}

	// 3. Check AUTO_REBUILD_AFTER_PUSH variable (NS-REQ-2).
	// Default is true (rebuild) when the variable is unset.
	if getVariable != nil {
		val, _ := getVariable("workspace", slug, "AUTO_REBUILD_AFTER_PUSH")
		if val == "false" {
			return
		}
	}

	// 4. Enqueue rebuild job with duplicate suppression (NS-REQ-1, NS-REQ-4).
	// Capture strategy at enqueue time, consistent with sync auto-rebuild.
	strategy := StrategyRebase
	if getVariable != nil {
		val, _ := getVariable("workspace", slug, "REBUILD_STRATEGY")
		if val != "" {
			strategy = val
		}
	}

	ib := ""
	if integrationBranch.Valid {
		ib = integrationBranch.String
	}

	payload := RebuildPayload{
		WorkspaceSlug:     slug,
		Strategy:          strategy,
		SubmittedBy:       "system:push-hook",
		IntegrationBranch: ib,
	}
	payloadJSON, _ := json.Marshal(payload)
	groupKey := FormatGroupKey(slug, ib)
	nonce := uuid.New().String()

	_, _, enqErr := queue.Enqueue(jobqueue.EnqueueParams{
		Type:        "rebuild",
		Key:         slug,
		Nonce:       nonce,
		Payload:     payloadJSON,
		SubmittedBy: "system:push-hook",
		Group:       groupKey,
	})
	if enqErr != nil {
		log.Printf("post-push hook: failed to enqueue rebuild for %q: %v", slug, enqErr)
	}
	// NS-REQ-4: If duplicate key is already queued or running, Enqueue returns
	// (existingID, true, nil) — the duplicate is silently suppressed.
}
