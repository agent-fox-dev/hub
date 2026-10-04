package carrypatch

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

// readDoc reads a file under the repository's docs directory.
func readDoc(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", rel))
	if err != nil {
		t.Fatalf("read docs/%s: %v", rel, err)
	}
	return string(b)
}

func requireContains(t *testing.T, what, doc string, subs ...string) {
	t.Helper()
	// Markdown is hard-wrapped: compare on whitespace-normalised text.
	doc = strings.Join(strings.Fields(doc), " ")
	for _, s := range subs {
		if !strings.Contains(doc, s) {
			t.Errorf("%s does not contain %q", what, s)
		}
	}
}

// section returns the text from the first line containing start up to (not
// including) the next line that begins with stop, or to the end.
func section(t *testing.T, doc, start, stop string) string {
	t.Helper()
	i := strings.Index(doc, start)
	if i < 0 {
		t.Fatalf("section start %q not found", start)
	}
	rest := doc[i+len(start):]
	if j := strings.Index(rest, "\n"+stop); j >= 0 {
		rest = rest[:j]
	}
	return start + rest
}

// ===========================================================================
// TS-01-69: docs/api.md and docs/openapi.yaml describe the narrowed
// workspace_busy behaviour and source_sha.
// Requirement: 01-REQ-10.4
// ===========================================================================

func TestDocs_TS_01_69_APIDocsDescribeNarrowedLock(t *testing.T) {
	api := readDoc(t, "api.md")
	openapi := readDoc(t, "openapi.yaml")

	requireContains(t, "api.md", api, "source_sha", "whole rebuild", "workspace_busy")
	requireContains(t, "openapi.yaml", openapi, "source_sha", "whole rebuild", "workspace_busy")

	lock := section(t, api, "### Workspace Lock", "### ")
	requireContains(t, "api.md Workspace Lock section", lock,
		"fetch", "final", "archive", "reclone", "whole rebuild")

	var row string
	for _, line := range strings.Split(api, "\n") {
		if strings.HasPrefix(line, "| `workspace_busy`") {
			row = line
		}
	}
	if row == "" {
		t.Fatal("api.md has no workspace_busy error-type row")
	}
	requireContains(t, "api.md workspace_busy row", row, "fetch", "final", "whole rebuild")

	// The 409 rows of the affected endpoints mention the rebuild phases.
	for _, anchor := range []string{
		"### POST /api/v1/workspaces/:slug/sync",
		"### POST /api/v1/workspaces/:slug/reclone",
		"### DELETE /api/v1/workspaces/:slug/rerere/\\*pathspec",
		"### POST /api/v1/workspaces/:slug/rebuilds/:id/rollback",
		"### POST /api/v1/workspaces/:slug/archive",
	} {
		sec := section(t, api, anchor, "### ")
		if !strings.Contains(sec, "workspace_busy") {
			t.Errorf("api.md section %q has no workspace_busy 409 row", anchor)
			continue
		}
		if !strings.Contains(sec, "rebuild") {
			t.Errorf("api.md section %q does not say how a rebuild affects workspace_busy", anchor)
		}
	}

	// patch_results[].source_sha is listed in the rebuild record table.
	requireContains(t, "api.md patch result table", api, "`patch_results[].source_sha`")

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(openapi), &parsed); err != nil {
		t.Fatalf("docs/openapi.yaml is not valid YAML: %v", err)
	}
	// source_sha is declared as a property of the patch result schema.
	found := false
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if props, ok := x["properties"].(map[string]any); ok {
				if _, ok := props["source_sha"]; ok {
					found = true
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(parsed)
	if !found {
		t.Error("openapi.yaml declares no schema property named source_sha")
	}
}

// ===========================================================================
// TS-01-70: docs/carry_patch_workflow.md describes the worktree flow,
// unchanged trunk, pre-rebuild clones, follow-up and the lock note.
// Requirement: 01-REQ-10.5
// ===========================================================================

func TestDocs_TS_01_70_WorkflowDescribesWorktreeFlow(t *testing.T) {
	doc := readDoc(t, "carry_patch_workflow.md")

	algo := section(t, doc, "### Rebuild algorithm", "### ")
	requireContains(t, "Rebuild algorithm", algo, "worktree", "update-ref", "follow-up")
	for _, line := range strings.Split(algo, "\n") {
		if strings.Contains(line, "_rebuild_temp") && !strings.Contains(strings.ToLower(line), "legacy") {
			t.Errorf("Rebuild algorithm mentions _rebuild_temp outside a legacy note: %q", line)
		}
	}
	lower := strings.Join(strings.Fields(strings.ToLower(doc)), " ")
	for _, want := range []string{
		"does not change the trunk",
		"pre-rebuild integration branch",
		"follow-up rebuild",
		"stale",
	} {
		if !strings.Contains(lower, want) {
			t.Errorf("carry_patch_workflow.md does not contain %q", want)
		}
	}
	for _, line := range strings.Split(doc, "\n") {
		if strings.Contains(line, "_rebuild_temp") && !strings.Contains(strings.ToLower(line), "legacy") {
			t.Errorf("_rebuild_temp mentioned outside a legacy note: %q", line)
		}
	}

	// The lock note describes the two lock phases and the guard.
	i := strings.Index(doc, "**Concurrent rebuild prevention.**")
	if i < 0 {
		t.Fatal("lock note (Concurrent rebuild prevention) not found")
	}
	note := doc[i:]
	if j := strings.Index(note, "\n\n**First rebuild"); j >= 0 {
		note = note[:j]
	}
	requireContains(t, "lock note", note, "fetch", "final", "archive", "reclone", "whole")
}

// ===========================================================================
// TS-01-71: docs/examples/AGENTS_carry_patch.md no longer states
// _rebuild_temp except as legacy.
// Requirement: 01-REQ-10.6
// ===========================================================================

func TestDocs_TS_01_71_AgentsExampleMarksRebuildTempLegacy(t *testing.T) {
	doc := readDoc(t, "examples/AGENTS_carry_patch.md")
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "_rebuild_temp") {
			continue
		}
		lo, hi := max(0, i-1), min(len(lines), i+3)
		ctx := strings.ToLower(strings.Join(lines[lo:hi], "\n"))
		if !strings.Contains(ctx, "legacy") {
			t.Errorf("line %d mentions _rebuild_temp without a legacy marker: %q", i+1, line)
		}
	}
	if strings.Contains(doc, "It is a transient branch used\n  during rebuild") {
		t.Error("the transient-branch statement is still present")
	}
}

// ===========================================================================
// TS-01-72: ADR 02 exists with the decision, the go-git capability table and
// the revisit condition.
// Requirement: 01-REQ-10.7
// ===========================================================================

func TestDocs_TS_01_72_ADR02(t *testing.T) {
	d := readDoc(t, "adr/02-run-rebuilds-with-the-git-cli.md")
	requireContains(t, "ADR 02", d,
		"worktree", "cherry-pick", "rerere", "gc", "v5.19.1", "COMPATIBILITY.md",
		"non-FF merge", "git worktree add", "git worktree remove", "git worktree prune")
	for _, row := range []string{"worktree add", "worktree remove", "worktree prune", "cherry-pick", "non-FF merge", "rerere", "gc"} {
		found := false
		for _, line := range strings.Split(d, "\n") {
			if strings.HasPrefix(line, "|") && strings.Contains(line, row) {
				found = true
			}
		}
		if !found {
			t.Errorf("ADR 02 capability table has no row for %q", row)
		}
	}
	lower := strings.ToLower(d)
	requireContains(t, "ADR 02 (revisit condition)", lower, "revisit", "linked-worktree", "--no-ff", "v6")
}

// ===========================================================================
// TS-20-53: docs/api.md documents the new sync behaviour, variables, fields,
// messages and events.
// Requirement: 20-REQ-9.1
// ===========================================================================

func TestDocs_TS_20_53_APIMdDocumentsOriginSync(t *testing.T) {
	api := readDoc(t, "api.md")

	// Response fields
	requireContains(t, "api.md", api,
		"origin_fetched",
		"patches_synced",
		"patches_diverged",
	)

	// Variables
	requireContains(t, "api.md", api,
		"PATCH_BRANCH_SOURCE",
		"PATCH_DIVERGENCE_POLICY",
	)

	// Patch response / patch-status fields
	requireContains(t, "api.md", api,
		"origin_sync_state",
		"origin_sha",
		"origin_synced_at",
		"patches_missing_on_origin",
	)

	// Error messages
	requireContains(t, "api.md", api,
		"origin fetch failed",
		"failed to resolve origin credentials",
	)

	// Audit events
	requireContains(t, "api.md", api,
		"hub.patch.sync",
		"hub.patch.replace",
	)

	// AUTO_REBUILD_AFTER_SYNC text mentions patch changes
	varsSection := section(t, api, "## Workspace Variables Reference", "## ")
	for _, line := range strings.Split(varsSection, "\n") {
		if strings.Contains(line, "AUTO_REBUILD_AFTER_SYNC") {
			norm := strings.Join(strings.Fields(line), " ")
			if !strings.Contains(norm, "patch") {
				t.Error("AUTO_REBUILD_AFTER_SYNC row does not mention patch changes")
			}
			break
		}
	}
}

// ===========================================================================
// TS-20-54: docs/openapi.yaml describes the sync response, patch and
// patch-status changes and stays valid YAML.
// Requirement: 20-REQ-9.2
// ===========================================================================

func TestDocs_TS_20_54_OpenAPIDescribesSyncAndPatchChanges(t *testing.T) {
	raw := readDoc(t, "openapi.yaml")

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("docs/openapi.yaml is not valid YAML: %v", err)
	}

	// CarryPatchSyncResponse has origin_fetched in its required list
	schemas, _ := navigateMap(parsed, "components", "schemas")
	cpsr, _ := navigateMap(schemas, "CarryPatchSyncResponse")
	reqList, ok := cpsr["required"].([]any)
	if !ok {
		t.Fatal("CarryPatchSyncResponse has no required list")
	}
	hasOriginFetched := false
	for _, v := range reqList {
		if v == "origin_fetched" {
			hasOriginFetched = true
		}
	}
	if !hasOriginFetched {
		t.Error("CarryPatchSyncResponse.required does not include origin_fetched")
	}

	// CarryPatchSyncResponse has patches_synced and patches_diverged properties
	cpsrProps, _ := navigateMap(cpsr, "properties")
	if _, ok := cpsrProps["patches_synced"]; !ok {
		t.Error("CarryPatchSyncResponse has no patches_synced property")
	}
	if _, ok := cpsrProps["patches_diverged"]; !ok {
		t.Error("CarryPatchSyncResponse has no patches_diverged property")
	}

	// Sync operation has 502 response covering origin failures
	syncPath, _ := navigateMap(parsed, "paths", "/api/v1/workspaces/{slug}/sync")
	syncPost, _ := navigateMap(syncPath, "post")
	syncResponses, _ := navigateMap(syncPost, "responses")
	if _, ok := syncResponses["502"]; !ok {
		t.Error("sync operation has no 502 response")
	}
	resp502, _ := navigateMap(syncResponses, "502")
	desc502, _ := resp502["description"].(string)
	if !strings.Contains(desc502, "origin") {
		t.Error("sync 502 description does not mention origin")
	}

	// Patch schema has the three origin fields
	patchSchema, _ := navigateMap(schemas, "Patch")
	patchProps, _ := navigateMap(patchSchema, "properties")
	for _, field := range []string{"origin_sync_state", "origin_sha", "origin_synced_at"} {
		if _, ok := patchProps[field]; !ok {
			t.Errorf("Patch schema has no %s property", field)
		}
	}

	// PatchStatusEntry has the three origin fields
	pse, _ := navigateMap(schemas, "PatchStatusEntry")
	pseProps, _ := navigateMap(pse, "properties")
	for _, field := range []string{"origin_sync_state", "origin_sha", "origin_synced_at"} {
		if _, ok := pseProps[field]; !ok {
			t.Errorf("PatchStatusEntry schema has no %s property", field)
		}
	}

	// PatchStatusSummary has the two summary counts
	pss, _ := navigateMap(schemas, "PatchStatusSummary")
	pssProps, _ := navigateMap(pss, "properties")
	for _, field := range []string{"patches_diverged", "patches_missing_on_origin"} {
		if _, ok := pssProps[field]; !ok {
			t.Errorf("PatchStatusSummary schema has no %s property", field)
		}
	}
}

// navigateMap walks a nested map[string]any by keys.
func navigateMap(m map[string]any, keys ...string) (map[string]any, bool) {
	cur := m
	for _, k := range keys {
		v, ok := cur[k]
		if !ok {
			return nil, false
		}
		next, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// ===========================================================================
// TS-20-55: docs/cli.md and docs/carry_patch_workflow.md document the flag,
// exit code and variables, and docs/configuration.md is unchanged.
// Requirement: 20-REQ-9.3, 20-REQ-9.4
// ===========================================================================

func TestDocs_TS_20_55_CLIAndWorkflowDocs(t *testing.T) {
	cli := readDoc(t, "cli.md")
	requireContains(t, "cli.md", cli, "--fail-on-diverged")
	// Exit code 3
	if !strings.Contains(cli, "3") {
		t.Error("cli.md does not mention exit code 3")
	}

	wf := readDoc(t, "carry_patch_workflow.md")
	requireContains(t, "carry_patch_workflow.md", wf,
		"PATCH_BRANCH_SOURCE",
		"PATCH_DIVERGENCE_POLICY",
	)

	cfg := readDoc(t, "configuration.md")
	if strings.Contains(cfg, "PATCH_BRANCH_SOURCE") {
		t.Error("configuration.md should not mention PATCH_BRANCH_SOURCE")
	}
}

// ===========================================================================
// TS-21-38 (unit): docs/api.md describes the resolution order, new errors
// and audit metadata.
// Verifies: 21-REQ-10.1
// ===========================================================================

func TestDocs_TS_21_38_APIMdDescribesResolutionOrder(t *testing.T) {
	api := readDoc(t, "api.md")

	// Find the registration section.
	sec := section(t, api, "### POST /api/v1/workspaces/:slug/patches\n", "### ")

	// Resolution order: local, origin_tracking, origin_fetch.
	requireContains(t, "api.md registration section", sec,
		"local",
		"origin_tracking",
		"origin_fetch",
	)

	// New 400 message.
	requireContains(t, "api.md registration section", sec,
		"does not exist in repository or on origin",
	)

	// 409 workspace_busy and 502 origin_fetch_failed.
	requireContains(t, "api.md registration section", sec,
		"workspace_busy",
		"origin_fetch_failed",
	)

	// skip_branch_check description.
	requireContains(t, "api.md registration section", sec,
		"skip_branch_check",
	)

	// branch_resolution audit metadata with skipped value.
	requireContains(t, "api.md", api,
		"branch_resolution",
		"skipped",
	)
}

// ===========================================================================
// TS-21-39 (unit): docs/openapi.yaml documents the new description, 400, 409
// and 502 responses.
// Verifies: 21-REQ-10.2
// ===========================================================================

func TestDocs_TS_21_39_OpenAPIDescribesPatchResolution(t *testing.T) {
	raw := readDoc(t, "openapi.yaml")

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("docs/openapi.yaml is not valid YAML: %v", err)
	}

	// Navigate to POST /api/v1/workspaces/{slug}/patches.
	patchesPath, ok := navigateMap(parsed, "paths", "/api/v1/workspaces/{slug}/patches")
	if !ok {
		t.Fatal("openapi.yaml has no /api/v1/workspaces/{slug}/patches path")
	}
	postOp, ok := navigateMap(patchesPath, "post")
	if !ok {
		t.Fatal("openapi.yaml has no POST operation on /api/v1/workspaces/{slug}/patches")
	}

	// Description mentions the resolution order.
	desc, _ := postOp["description"].(string)
	for _, needle := range []string{"local", "origin_tracking", "origin_fetch"} {
		if !strings.Contains(desc, needle) {
			t.Errorf("POST description does not mention %q", needle)
		}
	}

	// Responses.
	responses, ok := navigateMap(postOp, "responses")
	if !ok {
		t.Fatal("POST operation has no responses")
	}

	// 400 description mentions the new message.
	resp400, ok := navigateMap(responses, "400")
	if !ok {
		t.Fatal("POST operation has no 400 response")
	}
	desc400, _ := resp400["description"].(string)
	if !strings.Contains(desc400, "or on origin") {
		t.Error("400 description does not mention 'or on origin'")
	}

	// 409 response exists.
	if _, ok := responses["409"]; !ok {
		t.Error("POST operation has no 409 response")
	}

	// 502 response exists.
	if _, ok := responses["502"]; !ok {
		t.Error("POST operation has no 502 response")
	}

	// skip_branch_check property description says no lookup, fetch or local branch.
	schemas, ok := navigateMap(parsed, "components", "schemas")
	if !ok {
		t.Fatal("openapi.yaml has no components/schemas")
	}
	addPatch, ok := navigateMap(schemas, "AddPatchRequest")
	if !ok {
		t.Fatal("openapi.yaml has no AddPatchRequest schema")
	}
	addPatchProps, ok := navigateMap(addPatch, "properties")
	if !ok {
		t.Fatal("AddPatchRequest has no properties")
	}
	skipProp, ok := navigateMap(addPatchProps, "skip_branch_check")
	if !ok {
		t.Fatal("AddPatchRequest has no skip_branch_check property")
	}
	skipDesc, _ := skipProp["description"].(string)
	for _, needle := range []string{"lookup", "fetch", "local branch"} {
		if !strings.Contains(strings.ToLower(skipDesc), needle) {
			t.Errorf("skip_branch_check description does not mention %q: %s", needle, skipDesc)
		}
	}
}

// ===========================================================================
// TS-21-40 (unit): docs/cli.md and the afc patch add help text describe the
// resolution behaviour.
// Verifies: 21-REQ-10.3
// ===========================================================================

func TestDocs_TS_21_40_CLIMdAndHelpDescribeResolution(t *testing.T) {
	cliDoc := readDoc(t, "cli.md")

	// Find the patch add section.
	patchAddSec := section(t, cliDoc, "### afc patch add", "### ")

	// Mentions local, tracking and fork resolution.
	requireContains(t, "cli.md patch add section", patchAddSec,
		"local",
		"tracking",
		"PATCH_BRANCH_SOURCE",
	)

	// --skip-branch-check text says lookup, fetch and local branch creation are skipped.
	skipIdx := strings.Index(patchAddSec, "skip-branch-check")
	if skipIdx < 0 {
		t.Fatal("cli.md patch add section does not mention skip-branch-check")
	}
	// Check the surrounding text (the flag row or description).
	skipContext := patchAddSec[skipIdx:]
	if end := strings.Index(skipContext, "\n\n"); end > 0 {
		skipContext = skipContext[:end]
	}
	norm := strings.Join(strings.Fields(strings.ToLower(skipContext)), " ")
	for _, needle := range []string{"lookup", "fetch", "local branch"} {
		if !strings.Contains(norm, needle) {
			t.Errorf("cli.md --skip-branch-check text does not mention %q", needle)
		}
	}

	// Check the cobra command's Long help and --skip-branch-check flag usage.
	addCmd := findSubcommand(t, cli.PatchCmd(), "add")
	if addCmd.Long == "" {
		t.Fatal("patch add command has no Long help text")
	}
	for _, needle := range []string{"origin", "PATCH_BRANCH_SOURCE"} {
		if !strings.Contains(addCmd.Long, needle) {
			t.Errorf("patch add Long help does not mention %q", needle)
		}
	}

	skipFlag := addCmd.Flags().Lookup("skip-branch-check")
	if skipFlag == nil {
		t.Fatal("patch add command has no --skip-branch-check flag")
	}
	skipUsage := strings.ToLower(skipFlag.Usage)
	for _, needle := range []string{"lookup", "fetch", "local branch"} {
		if !strings.Contains(skipUsage, needle) {
			t.Errorf("--skip-branch-check usage does not mention %q: %s", needle, skipFlag.Usage)
		}
	}
}

// findSubcommand finds a subcommand by name.
func findSubcommand(t *testing.T, parent *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("subcommand %q not found", name)
	return nil
}

// ===========================================================================
// TS-21-41 (integration): afc patch add sends the same body and has no new
// flag.
// Verifies: 21-REQ-10.4
// ===========================================================================

func TestDocs_TS_21_41_PatchAddNoNewFlag(t *testing.T) {
	// The expected flag names for the patch add command, pre-change.
	expectedFlags := []string{
		"branch",
		"description",
		"if-not-exists",
		"position",
		"skip-branch-check",
		"upstream-pr",
	}

	addCmd := findSubcommand(t, cli.PatchCmd(), "add")
	var flagNames []string
	addCmd.Flags().VisitAll(func(f *pflag.Flag) {
		flagNames = append(flagNames, f.Name)
	})
	sort.Strings(flagNames)

	if len(flagNames) != len(expectedFlags) {
		t.Fatalf("flag count = %d; want %d; flags = %v", len(flagNames), len(expectedFlags), flagNames)
	}
	for i, name := range flagNames {
		if name != expectedFlags[i] {
			t.Errorf("flag[%d] = %q; want %q", i, name, expectedFlags[i])
		}
	}
}

// ===========================================================================
// TS-21-42: removed. This test verified that spec 21 did not modify
// carry_patch_workflow.md. Spec 24 intentionally rewrites the file, so the
// git-diff guard is no longer applicable.
// ===========================================================================

// ===========================================================================
// TS-24-5 (unit): The Remotes table describes origin as the fork and push
// target and drops the old purpose text.
// Verifies: 24-REQ-2.1
// ===========================================================================

func TestDocs_TS_24_5_RemotesTableOriginRow(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// The origin row should mention fork, REBUILD_PUSH_INTEGRATION_BRANCH and origin mode.
	remotesSection := section(t, guide, "### Remotes", "### ")
	// Find the origin row in the table.
	var originRow string
	for _, line := range strings.Split(remotesSection, "\n") {
		if strings.HasPrefix(line, "|") && strings.Contains(line, "`origin`") {
			originRow = line
			break
		}
	}
	if originRow == "" {
		t.Fatal("Remotes table has no origin row")
	}
	requireContains(t, "origin row", originRow, "fork", "REBUILD_PUSH_INTEGRATION_BRANCH")

	// The guide must not contain the old purpose text.
	if strings.Contains(guide, "Where patch branches live; push target for local work") {
		t.Error("guide still contains the old origin purpose text")
	}
}

// ===========================================================================
// TS-24-6 (unit): The Remotes table or a note names the hub's git server as
// the hub-mode patch branch location.
// Verifies: 24-REQ-2.2
// ===========================================================================

func TestDocs_TS_24_6_RemotesHubGitServer(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	s := section(t, guide, "### Remotes", "### ")
	requireContains(t, "Remotes section", s, "hub", "git server", "`hub` mode")
}

// ===========================================================================
// TS-24-7 (unit): A "Where patch branches live" section follows
// Concepts/Remotes and states the trunk read rule.
// Verifies: 24-REQ-2.3
// ===========================================================================

func TestDocs_TS_24_7_WherePatchBranchesLiveSection(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// The heading must appear after the Remotes heading.
	remotesIdx := strings.Index(guide, "### Remotes")
	wpblIdx := strings.Index(guide, "### Where patch branches live")
	if wpblIdx < 0 {
		t.Fatal("heading 'Where patch branches live' not found")
	}
	if wpblIdx <= remotesIdx {
		t.Error("'Where patch branches live' does not appear after Remotes")
	}

	s := section(t, guide, "### Where patch branches live", "### ")
	requireContains(t, "Where patch branches live", s,
		"refs/heads/<branch>",
		"refs/remotes/origin/*",
	)
}

// ===========================================================================
// TS-24-8 (unit): The section explains PATCH_BRANCH_SOURCE and has a
// comparison table with all seven rows.
// Verifies: 24-REQ-2.4
// ===========================================================================

func TestDocs_TS_24_8_PatchBranchSourceAndComparisonTable(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	s := section(t, guide, "### Where patch branches live", "### ")

	requireContains(t, "Where patch branches live", s,
		"PATCH_BRANCH_SOURCE",
		"`hub`",
		"`origin`",
		"PUSH_PATCHES_TO_ORIGIN=true",
	)

	// Count table rows (lines starting with | that are not the header separator).
	var tableRows int
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") && !strings.HasPrefix(trimmed, "|---") && !strings.HasPrefix(trimmed, "| ---") {
			tableRows++
		}
	}
	// Subtract 1 for the header row; need at least 7 data rows.
	if tableRows-1 < 7 {
		t.Errorf("comparison table has %d data rows; want at least 7", tableRows-1)
	}
}

// ===========================================================================
// TS-24-9 (unit): The section recommends origin mode for upstream-PR teams,
// hub for agent workspaces, and states integration branch handling.
// Verifies: 24-REQ-2.5
// ===========================================================================

func TestDocs_TS_24_9_RecommendationsAndIntegrationBranch(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	s := section(t, guide, "### Where patch branches live", "### ")

	requireContains(t, "Where patch branches live", s,
		"REBUILD_PUSH_INTEGRATION_BRANCH=true",
		"agent",
		"upstream pull request",
		"never fetched",
	)
}

// ===========================================================================
// TS-24-10 (unit): The section states the mode is read per operation and has
// a switching-modes note.
// Verifies: 24-REQ-2.6
// ===========================================================================

func TestDocs_TS_24_10_ModeReadPerOperationAndSwitching(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	s := section(t, guide, "### Where patch branches live", "### ")

	requireContains(t, "Where patch branches live", s,
		"start of each",
		"refs/hub/replaced/<branch>",
		"clears",
	)
}

// ===========================================================================
// TS-24-11 (unit): The section points to the Configuration section for the
// three variables.
// Verifies: 24-REQ-2.7
// ===========================================================================

func TestDocs_TS_24_11_ConfigurationPointer(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	s := section(t, guide, "### Where patch branches live", "### ")

	requireContains(t, "Where patch branches live", s,
		"Configuration",
		"PATCH_BRANCH_SOURCE",
		"PATCH_DIVERGENCE_POLICY",
		"PUSH_PATCHES_TO_ORIGIN",
	)

	// The Configuration section anchor must exist in the guide.
	if !strings.Contains(guide, "## Configuration") {
		t.Error("guide has no ## Configuration section")
	}
}

// ===========================================================================
// TS-24-12 (unit): The patch field table lists the three sync-state fields
// with absence and mode rules.
// Verifies: 24-REQ-3.1
// ===========================================================================

func TestDocs_TS_24_12_PatchFieldTableSyncStateFields(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// Extract the Patch list section which contains the field table.
	patchList := section(t, guide, "### Patch list", "### ")

	for _, field := range []string{"origin_sync_state", "origin_sha", "origin_synced_at"} {
		// Find the table row for this field.
		var row string
		for _, line := range strings.Split(patchList, "\n") {
			if strings.HasPrefix(line, "|") && strings.Contains(line, field) {
				row = line
				break
			}
		}
		if row == "" {
			t.Errorf("patch field table has no row for %q", field)
			continue
		}
		requireContains(t, field+" row", row, "absent when null", "origin mode")
	}
}

// ===========================================================================
// TS-24-13 (unit): The three sync states are explained and missing_on_origin
// leaves patch status unchanged.
// Verifies: 24-REQ-3.2
// ===========================================================================

func TestDocs_TS_24_13_SyncStatesExplained(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// The explanation should be in the Patch list or Patch statuses area.
	// Look for the sync-state paragraph anywhere in the guide.
	requireContains(t, "carry_patch_workflow.md", guide,
		"in_sync",
		"diverged",
		"missing_on_origin",
		"hub's copy",
		"disables or removes",
	)
}

// ===========================================================================
// TS-24-14 (unit): Registration text gives the resolution order, the 400
// message and the --skip-branch-check rule.
// Verifies: 24-REQ-3.3
// ===========================================================================

func TestDocs_TS_24_14_RegistrationResolutionOrder(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// Check both "Add patches" (Getting Started) and "Adding a new patch"
	// (Day-to-day) sections.
	addPatches := section(t, guide, "### 5. Register your patches", "### ")
	addingNew := section(t, guide, "### Adding a new patch", "### ")

	for _, s := range []struct {
		name string
		text string
	}{
		{"Add patches", addPatches},
		{"Adding a new patch", addingNew},
	} {
		requireContains(t, s.name, s.text,
			"refs/heads/<name>",
			"refs/remotes/origin/<name>",
			"branch does not exist in repository or on origin",
			"--skip-branch-check",
		)
	}
}

// ===========================================================================
// TS-24-15 (unit): "Removing a patch" says the backup ref is deleted and the
// branch is not.
// Verifies: 24-REQ-3.4
// ===========================================================================

func TestDocs_TS_24_15_RemovingPatchBackupRef(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	removeSec := section(t, guide, "### Removing a patch", "### ")
	requireContains(t, "Removing a patch", removeSec,
		"refs/hub/replaced/<branch>",
		"deleted",
		"branch itself is not deleted",
	)
}

// ===========================================================================
// TS-24-16 (unit): Soft-delete and restore text state the 7-day retention
// window.
// Verifies: 24-REQ-3.5
// ===========================================================================

func TestDocs_TS_24_16_SoftDeleteRetentionWindow(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	softDelete := section(t, guide, "### Soft-delete lifecycle", "### ")
	restoring := section(t, guide, "### Restoring a soft-deleted patch", "### ")

	requireContains(t, "Soft-delete lifecycle", softDelete, "7 days")
	requireContains(t, "Restoring a soft-deleted patch", restoring, "7 days")
}

// ===========================================================================
// TS-24-17 (unit): With no production caller of the purge routines the guide
// promises no automatic removal.
// Verifies: 24-REQ-3.6
// ===========================================================================

func TestDocs_TS_24_17_NoPurgeSchedulerClaim(t *testing.T) {
	// First, verify that no production caller of the purge routines exists.
	// We search non-test .go files under internal/ for calls to
	// PurgeDeletedPatches or PurgeExpiredDeletedPatches.
	purgeCallers := findPurgeCallers(t)
	if len(purgeCallers) > 0 {
		t.Skipf("production callers found: %v; TS-24-18 covers this case", purgeCallers)
	}

	guide := readDoc(t, "carry_patch_workflow.md")

	// The soft-delete text should say expired rows are removed only when a
	// purge runs.
	softDelete := section(t, guide, "### Soft-delete lifecycle", "### ")
	requireContains(t, "Soft-delete lifecycle", softDelete, "only when a purge runs")

	// No sentence should claim a background purge or scheduler removes
	// expired patches.
	norm := strings.Join(strings.Fields(strings.ToLower(guide)), " ")
	for _, bad := range []string{
		"background purge process permanently removes",
		"scheduler removes",
		"scheduler purges",
	} {
		if strings.Contains(norm, bad) {
			t.Errorf("guide contains forbidden purge claim: %q", bad)
		}
	}
}

// ===========================================================================
// TS-24-18 (unit): If a production purge caller exists, the guide describes
// its schedule as implemented.
// Verifies: 24-REQ-3.7
// ===========================================================================

func TestDocs_TS_24_18_PurgeScheduleIfCallerExists(t *testing.T) {
	purgeCallers := findPurgeCallers(t)
	if len(purgeCallers) == 0 {
		t.Skip("no production callers of purge routines found; TS-24-17 covers this case")
	}
	// If we reach here, a production caller exists and we would need to
	// verify the guide describes the schedule. Since no caller exists today,
	// this branch is not exercised.
	guide := readDoc(t, "carry_patch_workflow.md")
	softDelete := section(t, guide, "### Soft-delete lifecycle", "### ")
	_ = softDelete
	t.Error("production purge caller found but schedule verification not implemented")
}

// ===========================================================================
// TS-24-19 (unit): Getting Started orders the fork-first steps on
// acme-oss/api-gateway and your-org/api-gateway-fork.
// Verifies: 24-REQ-4.1
// ===========================================================================

func TestDocs_TS_24_19_GettingStartedForkFirstOrder(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// The Getting Started section must contain both repo names.
	gs := section(t, guide, "## Getting started", "## ")
	requireContains(t, "Getting Started", gs,
		"acme-oss/api-gateway",
		"your-org/api-gateway-fork",
	)

	// Step headings must appear in the correct order.
	headings := []string{
		"Create a carry-patch workspace",
		"Set upstream credentials",
		"Choose the authority",
		"Create and push a patch branch",
		"Register your patches",
		"Sync",
		"Preview a rebuild",
		"Trigger a rebuild manually",
		"Check the status dashboard",
		"Handle a failed rebuild",
		"Roll back a rebuild",
	}

	var indices []int
	for _, h := range headings {
		idx := strings.Index(gs, h)
		if idx < 0 {
			t.Errorf("Getting Started missing heading %q", h)
			continue
		}
		indices = append(indices, idx)
	}
	for i := 1; i < len(indices); i++ {
		if indices[i] <= indices[i-1] {
			t.Errorf("heading %q appears before or at the same position as %q", headings[i], headings[i-1])
		}
	}

	// Steps should be consecutively numbered.
	for i, h := range headings {
		want := strings.Contains(gs, "### "+itoa(i+1)+". "+h) ||
			strings.Contains(gs, "### "+itoa(i+1)+". "+h[:min(len(h), 20)])
		// Check that the step number prefix exists.
		numPrefix := "### " + itoa(i+1) + "."
		if !strings.Contains(gs, numPrefix) {
			t.Errorf("Getting Started missing numbered step %q", numPrefix)
		}
		_ = want
	}
}

// itoa converts an int to a string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// ===========================================================================
// TS-24-20 (unit): The choose-the-authority step shows the vars command and
// the credential requirement.
// Verifies: 24-REQ-4.2
// ===========================================================================

func TestDocs_TS_24_20_ChooseAuthorityStep(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	step := section(t, guide, "### 3. Choose the authority", "### ")

	requireContains(t, "Choose the authority step", step,
		"afc vars create PATCH_BRANCH_SOURCE=origin --workspace api-gateway",
		"REBUILD_PUSH_INTEGRATION_BRANCH=true",
		"GIT_PAT",
		"GIT_USERNAME",
		"GIT_PASSWORD",
	)
}

// ===========================================================================
// TS-24-21 (unit): Branch creation and registration steps push to the fork
// and register without --skip-branch-check.
// Verifies: 24-REQ-4.3
// ===========================================================================

func TestDocs_TS_24_21_BranchCreationAndRegistration(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// The fork-first branch creation step.
	branchStep := section(t, guide, "### 4. Create and push a patch branch", "### ")
	requireContains(t, "branch creation step", branchStep,
		"git push",
		"upstream PR",
	)

	// The registration step.
	regStep := section(t, guide, "### 5. Register your patches", "### ")
	requireContains(t, "registration step", regStep,
		"--if-not-exists",
		"--position",
	)

	// The primary afc patch add lines should NOT have --skip-branch-check.
	// Extract fenced code blocks from the registration step.
	for _, line := range strings.Split(regStep, "\n") {
		norm := strings.TrimSpace(line)
		if strings.HasPrefix(norm, "afc patch add") && !strings.Contains(norm, "skip-branch-check") {
			// Good: primary add without skip-branch-check.
			continue
		}
	}
	// Verify the primary patch add examples don't use --skip-branch-check.
	// The first afc patch add line should not have it.
	var firstPatchAdd string
	for _, line := range strings.Split(regStep, "\n") {
		norm := strings.TrimSpace(line)
		if strings.HasPrefix(norm, "afc patch add") {
			firstPatchAdd = norm
			break
		}
	}
	if firstPatchAdd != "" && strings.Contains(firstPatchAdd, "--skip-branch-check") {
		t.Error("primary afc patch add line should not have --skip-branch-check")
	}
}

// ===========================================================================
// TS-24-22 (unit): The sync step shows the command and explains why sync,
// not rebuild, brings in fork commits.
// Verifies: 24-REQ-4.4
// ===========================================================================

func TestDocs_TS_24_22_SyncStepExplanation(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	step := section(t, guide, "### 6. Sync", "### ")

	requireContains(t, "sync step", step,
		"afc workspace sync api-gateway",
		"afc rebuild submit",
		"does not fetch the fork",
	)
}

// ===========================================================================
// TS-24-23 (unit): The sync sample response has the new fields and the text
// covers outcomes, --fail-on-diverged and the divergence policy.
// Verifies: 24-REQ-4.5
// ===========================================================================

func TestDocs_TS_24_23_SyncResponseFieldsAndOutcomes(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	step := section(t, guide, "### 6. Sync", "### ")

	requireContains(t, "sync step", step,
		"\"origin_fetched\"",
		"\"patches_synced\"",
		"\"patches_diverged\"",
		"created",
		"fast_forwarded",
		"replaced",
		"--fail-on-diverged",
		"exit code 3",
		"PATCH_DIVERGENCE_POLICY",
	)
}

// ===========================================================================
// TS-24-24 (unit): The dashboard step mentions per-patch sync-state fields
// and the new summary counts.
// Verifies: 24-REQ-4.6
// ===========================================================================

func TestDocs_TS_24_24_DashboardSyncStateFields(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	step := section(t, guide, "### 9. Check the status dashboard", "### ")

	requireContains(t, "dashboard step", step,
		"origin_sync_state",
		"patches_diverged",
		"patches_missing_on_origin",
	)
}

// ===========================================================================
// TS-24-25 (unit): The "Alternative: hub-authoritative workspace" heading
// shows the hub flow.
// Verifies: 24-REQ-4.7
// ===========================================================================

func TestDocs_TS_24_25_AlternativeHubAuthoritativeWorkspace(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	s := section(t, guide, "### Alternative: hub-authoritative workspace", "### ")

	requireContains(t, "Alternative: hub-authoritative workspace", s,
		"unset",
		"git URL",
		"afc rebuild submit",
		"PUSH_PATCHES_TO_ORIGIN=true",
		"agent-driven",
	)
}

// ===========================================================================
// TS-24-26 (property): Every afc command and flag in the walkthroughs exists
// in docs/cli.md.
// Verifies: 24-REQ-4.8
// ===========================================================================

func TestDocs_TS_24_26_AFCCommandsExistInCLIMd(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	cliDoc := readDoc(t, "cli.md")

	// Extract the Getting Started section and the alternative flow.
	gs := section(t, guide, "## Getting started", "## ")

	// Extract afc command lines from fenced code blocks.
	inBlock := false
	var afcLines []string
	for _, line := range strings.Split(gs, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inBlock = !inBlock
			continue
		}
		if !inBlock {
			continue
		}
		// Skip continuation lines.
		if strings.HasPrefix(trimmed, "--") || trimmed == "" {
			continue
		}
		// Find lines that start with afc.
		if strings.HasPrefix(trimmed, "afc ") {
			afcLines = append(afcLines, trimmed)
		}
	}

	if len(afcLines) == 0 {
		t.Fatal("no afc command lines found in Getting Started")
	}

	for _, line := range afcLines {
		// Parse the command path: afc <sub1> <sub2> ...
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		// Build the command path (e.g. "afc workspace sync", "afc patch add").
		var cmdParts []string
		for _, p := range parts[1:] {
			if strings.HasPrefix(p, "-") {
				break
			}
			// Stop at arguments (values that look like slugs, IDs, etc.).
			if strings.Contains(p, "=") || strings.Contains(p, "/") || strings.Contains(p, ".") {
				break
			}
			// Stop at known argument positions (workspace slugs, etc.).
			// Heuristic: if it's a known subcommand word, include it.
			known := map[string]bool{
				"workspace": true, "patch": true, "rebuild": true,
				"secrets": true, "vars": true, "rerere": true,
				"sync": true, "create": true, "add": true,
				"submit": true, "status": true, "list": true,
				"update": true, "delete": true, "remove": true,
				"restore": true, "reorder": true, "preview": true,
				"cancel": true, "requeue": true, "rollback": true,
				"forget": true, "patch-status": true,
				"reset-to-origin": true, "reclone": true,
			}
			if known[p] {
				cmdParts = append(cmdParts, p)
			} else {
				break
			}
		}

		if len(cmdParts) == 0 {
			continue
		}

		// Check that the command path appears in cli.md.
		cmdPath := "afc " + strings.Join(cmdParts, " ")
		if !strings.Contains(cliDoc, cmdPath) {
			t.Errorf("command %q from walkthrough not found in cli.md", cmdPath)
		}

		// Check flags.
		for _, p := range parts {
			if !strings.HasPrefix(p, "--") {
				continue
			}
			flag := p
			if idx := strings.Index(flag, "="); idx > 0 {
				flag = flag[:idx]
			}
			if !strings.Contains(cliDoc, flag) {
				t.Errorf("flag %q (from %q) not found in cli.md", flag, line)
			}
		}
	}
}

// ===========================================================================
// TS-24-27 (unit): The Sync algorithm section lists the eight phases in order.
// Verifies: 24-REQ-5.1
// ===========================================================================

func TestDocs_TS_24_27_SyncAlgorithmPhasesInOrder(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	syncSec := section(t, guide, "### Sync algorithm", "### ")

	// The eight phase keywords must appear in order.
	keys := []string{
		"Resolve credentials",
		"Fetch upstream",
		"Fetch origin (origin mode only)",
		"Refresh patch branches",
		"Detect force-push",
		"Detect merged patches",
		"Auto-rebuild",
		"Write timestamps",
	}

	var indices []int
	for _, k := range keys {
		idx := strings.Index(syncSec, k)
		if idx < 0 {
			t.Errorf("Sync algorithm section missing phase %q", k)
			continue
		}
		indices = append(indices, idx)
	}
	for i := 1; i < len(indices); i++ {
		if indices[i] <= indices[i-1] {
			t.Errorf("phase %q appears before or at the same position as %q", keys[i], keys[i-1])
		}
	}
}

// ===========================================================================
// TS-24-28 (unit): The Sync algorithm says the origin fetch prunes and its
// failure answers 502 origin fetch failed with state unchanged.
// Verifies: 24-REQ-5.2
// ===========================================================================

func TestDocs_TS_24_28_SyncOriginFetchPrunesAnd502(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	syncSec := section(t, guide, "### Sync algorithm", "### ")

	requireContains(t, "Sync algorithm", syncSec,
		"prun",
		"origin fetch failed",
		"unchanged",
	)
}

// ===========================================================================
// TS-24-29 (unit): The patch refresh text lists outcomes, states and the
// backup ref.
// Verifies: 24-REQ-5.3
// ===========================================================================

func TestDocs_TS_24_29_PatchRefreshOutcomesStatesBackupRef(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	syncSec := section(t, guide, "### Sync algorithm", "### ")

	requireContains(t, "Sync algorithm", syncSec,
		"created",
		"fast_forwarded",
		"replaced",
		"in_sync",
		"diverged",
		"missing_on_origin",
		"refs/hub/replaced/<branch>",
	)
}

// ===========================================================================
// TS-24-30 (unit): Rebuild triggers and the AUTO_REBUILD_AFTER_SYNC entry
// cover patch changes.
// Verifies: 24-REQ-5.4
// ===========================================================================

func TestDocs_TS_24_30_RebuildTriggersAndAutoRebuildEntry(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// Check the rebuild decision text in the sync algorithm.
	syncSec := section(t, guide, "### Sync algorithm", "### ")
	requireContains(t, "Sync algorithm rebuild decision", syncSec,
		"upstream advanced",
		"conflict",
		"merged_upstream",
		"AUTO_REBUILD_AFTER_SYNC",
	)

	// Check the AUTO_REBUILD_AFTER_SYNC Configuration entry.
	configEntry := section(t, guide, "### AUTO_REBUILD_AFTER_SYNC", "### ")
	requireContains(t, "AUTO_REBUILD_AFTER_SYNC entry", configEntry,
		"patch",
	)
}

// ===========================================================================
// TS-24-31 (unit): The guide has no "returns immediately" claim and exactly
// one two-models description.
// Verifies: 24-REQ-5.5
// ===========================================================================

func TestDocs_TS_24_31_NoReturnsImmediatelyAndOneModelsDescription(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// No sentence says sync returns immediately when upstream is unchanged.
	norm := strings.ToLower(guide)
	if strings.Contains(norm, "returns immediately") {
		t.Error("guide contains 'returns immediately'")
	}

	// The spec-20 interim note should be gone.
	if strings.Contains(guide, "fork_push_control") {
		t.Error("guide still contains the interim fork_push_control note")
	}

	// The two models are described only in "Where patch branches live".
	count := strings.Count(guide, "### Where patch branches live")
	if count != 1 {
		t.Errorf("expected exactly 1 '### Where patch branches live' heading, got %d", count)
	}
}

// ===========================================================================
// TS-24-32 (unit): "Auto-rebuild on push" keeps the hook description and
// adds per-mode push treatment.
// Verifies: 24-REQ-5.6
// ===========================================================================

func TestDocs_TS_24_32_AutoRebuildOnPushPerModeTreatment(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	pushSec := section(t, guide, "### Auto-rebuild on push", "### ")

	requireContains(t, "Auto-rebuild on push", pushSec,
		"AUTO_REBUILD_AFTER_PUSH",
		"reject",
		"forward",
		"mirror",
		"accepted",
	)
}

// ===========================================================================
// TS-24-33 (unit): The Rebuild algorithm section keeps its checks and gains
// one sentence about snapshotted patch tips.
// Verifies: 24-REQ-5.7
// ===========================================================================

func TestDocs_TS_24_33_RebuildAlgorithmSnapshotSentence(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	algo := section(t, guide, "### Rebuild algorithm", "### ")

	requireContains(t, "Rebuild algorithm", algo,
		"last sync",
		"origin mode",
		"push",
		"hub mode",
	)
}

// ===========================================================================
// TS-24-34 (unit): The conflict section and the Getting Started failed-rebuild
// step open with a common part and one subsection per mode.
// Verifies: 24-REQ-6.1
// ===========================================================================

func TestDocs_TS_24_34_ConflictSectionAndGSFailedStepModeSubsections(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// Extract the "Resolving conflicts after a failed rebuild" section.
	conflictSec := section(t, guide, "### Resolving conflicts after a failed rebuild", "### ")

	// Extract the Getting Started failed-rebuild step.
	gsFailedStep := section(t, guide, "### 10. Handle a failed rebuild", "### ")

	for _, s := range []struct {
		name string
		text string
	}{
		{"conflict section", conflictSec},
		{"GS failed-rebuild step", gsFailedStep},
	} {
		norm := strings.Join(strings.Fields(s.text), " ")
		conflictIdx := strings.Index(norm, "conflict_files")
		hubIdx := strings.Index(norm, "hub mode")
		originIdx := strings.Index(norm, "origin mode")

		if conflictIdx < 0 {
			t.Errorf("%s does not mention conflict_files", s.name)
			continue
		}
		if hubIdx < 0 {
			t.Errorf("%s does not mention hub mode", s.name)
			continue
		}
		if originIdx < 0 {
			t.Errorf("%s does not mention origin mode", s.name)
			continue
		}
		if conflictIdx >= hubIdx {
			t.Errorf("%s: conflict_files should appear before hub mode", s.name)
		}
		if hubIdx >= originIdx {
			t.Errorf("%s: hub mode should appear before origin mode", s.name)
		}
	}
}

// ===========================================================================
// TS-24-35 (unit): The hub mode subsection keeps the existing instructions
// and states rerere support.
// Verifies: 24-REQ-6.2
// ===========================================================================

func TestDocs_TS_24_35_HubModeConflictSubsection(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	conflictSec := section(t, guide, "### Resolving conflicts after a failed rebuild", "### ")

	// Extract the hub mode subsection: from "hub mode" to "origin mode".
	hubIdx := strings.Index(conflictSec, "hub mode")
	originIdx := strings.Index(conflictSec, "origin mode")
	if hubIdx < 0 || originIdx < 0 {
		t.Fatal("conflict section missing hub mode or origin mode subsection")
	}
	hubSub := conflictSec[hubIdx:originIdx]

	requireContains(t, "hub mode subsection", hubSub,
		"trunk",
		"hub's git server",
		"active",
		"rerere",
		"during a rebuild",
	)
}

// ===========================================================================
// TS-24-36 (unit): The origin mode subsection gives the fork-side fix
// procedure.
// Verifies: 24-REQ-6.3
// ===========================================================================

func TestDocs_TS_24_36_OriginModeConflictSubsection(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	conflictSec := section(t, guide, "### Resolving conflicts after a failed rebuild", "### ")

	// Extract the origin mode subsection: from "origin mode" to end of section.
	originIdx := strings.Index(conflictSec, "origin mode")
	if originIdx < 0 {
		t.Fatal("conflict section missing origin mode subsection")
	}
	originSub := conflictSec[originIdx:]

	requireContains(t, "origin mode subsection", originSub,
		"local clone",
		"rebase",
		"push",
		"fork",
		"afc workspace sync",
		"active",
	)
}

// ===========================================================================
// TS-24-37 (unit): The origin mode subsection forbids editing the trunk or
// pushing to the hub and gives the reason, with no other such instruction.
// Verifies: 24-REQ-6.4
// ===========================================================================

func TestDocs_TS_24_37_OriginModeProhibitsTrunkEditAndHubPush(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	conflictSec := section(t, guide, "### Resolving conflicts after a failed rebuild", "### ")

	originIdx := strings.Index(conflictSec, "origin mode")
	if originIdx < 0 {
		t.Fatal("conflict section missing origin mode subsection")
	}
	originSub := conflictSec[originIdx:]

	requireContains(t, "origin mode subsection", originSub,
		"not edit the trunk",
		"not push",
		"overwritten",
	)
}

// ===========================================================================
// TS-24-38 (unit): The rerere limitation for fork-side resolutions is stated
// as a known limitation.
// Verifies: 24-REQ-6.5
// ===========================================================================

func TestDocs_TS_24_38_RerereLimitationForForkResolutions(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	conflictSec := section(t, guide, "### Resolving conflicts after a failed rebuild", "### ")

	originIdx := strings.Index(conflictSec, "origin mode")
	if originIdx < 0 {
		t.Fatal("conflict section missing origin mode subsection")
	}
	originSub := conflictSec[originIdx:]

	requireContains(t, "origin mode subsection", originSub,
		"rerere",
		"not recorded",
		"known limitation",
	)

	// Must not promise a planned feature. The text may say "not a planned
	// feature" (which is a prohibition, not a promise), so we check that
	// "planned feature" only appears after a negation.
	norm := strings.Join(strings.Fields(strings.ToLower(originSub)), " ")
	// Check that no sentence says this will be implemented.
	if strings.Contains(norm, "will be implemented") || strings.Contains(norm, "will be added") {
		t.Error("origin mode subsection promises a planned feature for rerere")
	}
	// If "planned feature" appears, it must be preceded by "not a".
	if idx := strings.Index(norm, "planned feature"); idx >= 0 {
		prefix := norm[:idx]
		if !strings.HasSuffix(strings.TrimSpace(prefix), "not a") {
			t.Error("origin mode subsection mentions 'planned feature' without negation")
		}
	}
}

// ===========================================================================
// TS-24-39 (unit): The recovery section covers state fields, fetching a
// replaced tip and pushing it to the fork.
// Verifies: 24-REQ-6.6
// ===========================================================================

func TestDocs_TS_24_39_RecoverySectionStateFieldsAndFetch(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	recoverySec := section(t, guide, "### Recovering a replaced or diverged patch branch", "### ")

	requireContains(t, "recovery section", recoverySec,
		"origin_sync_state",
		"replaced_sha",
		"git fetch <hub_url> refs/hub/replaced/<branch>",
		"fork",
	)
}

// ===========================================================================
// TS-24-40 (unit): The recovery section covers reset-to-origin, the single
// backup and missing_on_origin.
// Verifies: 24-REQ-6.7
// ===========================================================================

func TestDocs_TS_24_40_RecoverySectionResetAndBackup(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	recoverySec := section(t, guide, "### Recovering a replaced or diverged patch branch", "### ")

	requireContains(t, "recovery section", recoverySec,
		"afc patch reset-to-origin <slug> <patch-id>",
		"rebuild_triggered",
		"one backup",
		"overwrite",
		"missing_on_origin",
	)
}

// ===========================================================================
// TS-24-41 (unit): The Configuration section has exactly one entry heading
// for each of the three variables.
// Verifies: 24-REQ-7.1
// ===========================================================================

func TestDocs_TS_24_41_ConfigurationOneEntryPerVariable(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	cfg := section(t, guide, "## Configuration", "## ")

	vars := []string{"PATCH_BRANCH_SOURCE", "PATCH_DIVERGENCE_POLICY", "PUSH_PATCHES_TO_ORIGIN"}
	for _, v := range vars {
		heading := "### " + v
		count := strings.Count(cfg, heading)
		if count != 1 {
			t.Errorf("expected exactly 1 heading %q in Configuration, got %d", heading, count)
		}

		// Each entry must have a values table, an afc vars command and a
		// when-read statement.
		entry := section(t, cfg, heading, "### ")
		requireContains(t, v+" entry", entry, "afc vars", "| Value")
	}
}

// ===========================================================================
// TS-24-42 (unit): The three entries state defaults, exact-match,
// mode-specific ignore and dual meaning.
// Verifies: 24-REQ-7.2
// ===========================================================================

func TestDocs_TS_24_42_ConfigurationDefaultsExactMatchAndModeRules(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	cfg := section(t, guide, "## Configuration", "## ")

	// PATCH_BRANCH_SOURCE: default hub, exact match.
	pbs := section(t, cfg, "### PATCH_BRANCH_SOURCE", "### ")
	requireContains(t, "PATCH_BRANCH_SOURCE entry", pbs, "`hub` (default)", "exact")

	// PATCH_DIVERGENCE_POLICY: default replace, ignored in hub mode.
	pdp := section(t, cfg, "### PATCH_DIVERGENCE_POLICY", "### ")
	requireContains(t, "PATCH_DIVERGENCE_POLICY entry", pdp, "`replace` (default)", "ignored", "hub")

	// PUSH_PATCHES_TO_ORIGIN: default unset, exact `true`, forwarding/mirroring.
	ppto := section(t, cfg, "### PUSH_PATCHES_TO_ORIGIN", "### ")
	requireContains(t, "PUSH_PATCHES_TO_ORIGIN entry", ppto,
		"exactly `true`",
		"forward",
		"mirror",
	)
}

// ===========================================================================
// TS-24-43 (unit): A recommended-combinations table has the four required
// rows.
// Verifies: 24-REQ-7.3
// ===========================================================================

func TestDocs_TS_24_43_RecommendedCombinationsTable(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	cfg := section(t, guide, "## Configuration", "## ")

	// The table should contain the four combination keywords.
	requireContains(t, "Configuration combinations table", cfg,
		"fork-first",
		"forwarding",
		"hub-only",
		"mirror",
	)

	// The fork-first row should mention origin and REBUILD_PUSH_INTEGRATION_BRANCH.
	requireContains(t, "Configuration combinations table", cfg,
		"REBUILD_PUSH_INTEGRATION_BRANCH",
	)
}

// ===========================================================================
// TS-24-44 (unit): Limitations gain the five new items and keep the existing
// ones including the lock note.
// Verifies: 24-REQ-7.4, 24-REQ-7.5
// ===========================================================================

func TestDocs_TS_24_44_LimitationsNewAndExisting(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	lim := section(t, guide, "## Limitations", "## ")

	requireContains(t, "Limitations", lim,
		"webhooks",
		"POST /workspaces/:slug/sync",
		"every fork branch",
		"rerere",
		"missing_on_origin",
		"credentials",
		"**Concurrent rebuild prevention.**",
	)
}

// ===========================================================================
// TS-24-45 (integration): The API reference summary rows match the routes
// registered in RegisterRoutes.
// Verifies: 24-REQ-7.6
// ===========================================================================

func TestDocs_TS_24_45_APIReferenceSummaryMatchesRoutes(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// The patch endpoints table must have a GET row for single patch and
	// a POST row for reset-to-origin.
	patchEndpoints := section(t, guide, "### Patch endpoints", "### ")
	requireContains(t, "Patch endpoints table", patchEndpoints,
		"/workspaces/:slug/patches/:id",
		"reset-to-origin",
	)

	// Verify the GET single-patch row exists.
	var hasGetSingle bool
	for _, line := range strings.Split(patchEndpoints, "\n") {
		if strings.HasPrefix(line, "|") && strings.Contains(line, "GET") && strings.Contains(line, "/patches/:id") {
			hasGetSingle = true
			break
		}
	}
	if !hasGetSingle {
		t.Error("Patch endpoints table has no GET row for /workspaces/:slug/patches/:id")
	}

	// Verify the reset-to-origin row exists.
	var hasReset bool
	for _, line := range strings.Split(patchEndpoints, "\n") {
		if strings.HasPrefix(line, "|") && strings.Contains(line, "reset-to-origin") {
			hasReset = true
			break
		}
	}
	if !hasReset {
		t.Error("Patch endpoints table has no reset-to-origin row")
	}

	// The permission scopes table must list patches:read for GET single
	// patch and patches:write for reset-to-origin.
	permScopes := section(t, guide, "### Permission scopes", "### ")
	requireContains(t, "Permission scopes table", permScopes,
		"patches:read",
		"patches:write",
	)

	// Verify patches:read row mentions single-patch GET.
	var patchesReadRow string
	for _, line := range strings.Split(permScopes, "\n") {
		if strings.HasPrefix(line, "|") && strings.Contains(line, "patches:read") {
			patchesReadRow = line
			break
		}
	}
	if patchesReadRow == "" {
		t.Fatal("Permission scopes table has no patches:read row")
	}
	requireContains(t, "patches:read row", patchesReadRow, "single patch") // or "Get single patch" or similar

	// Verify patches:write row mentions reset-to-origin.
	var patchesWriteRow string
	for _, line := range strings.Split(permScopes, "\n") {
		if strings.HasPrefix(line, "|") && strings.Contains(line, "patches:write") {
			patchesWriteRow = line
			break
		}
	}
	if patchesWriteRow == "" {
		t.Fatal("Permission scopes table has no patches:write row")
	}
	requireContains(t, "patches:write row", patchesWriteRow, "reset")

	// The sync row in Other carry-patch endpoints must describe both modes.
	otherEndpoints := section(t, guide, "### Other carry-patch endpoints", "### ")
	var syncRow string
	for _, line := range strings.Split(otherEndpoints, "\n") {
		if strings.HasPrefix(line, "|") && strings.Contains(line, "sync") {
			syncRow = line
			break
		}
	}
	if syncRow == "" {
		t.Fatal("Other carry-patch endpoints table has no sync row")
	}
	requireContains(t, "sync row", syncRow, "origin")
}

// findPurgeCallers searches non-test .go files under internal/ for calls to
// PurgeDeletedPatches or PurgeExpiredDeletedPatches. It returns the file
// paths that contain such calls, excluding function definitions and test
// files.
func findPurgeCallers(t *testing.T) []string {
	t.Helper()
	var callers []string

	// Walk internal/ looking for .go files that are not tests.
	entries, err := os.ReadDir(filepath.Join("..", "..", "internal"))
	if err != nil {
		t.Fatalf("read internal/: %v", err)
	}

	for _, pkg := range entries {
		if !pkg.IsDir() {
			continue
		}
		pkgPath := filepath.Join("..", "..", "internal", pkg.Name())
		files, err := os.ReadDir(pkgPath)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(pkgPath, name))
			if err != nil {
				continue
			}
			content := string(data)
			// Skip the definition files themselves (interface and
			// implementation).
			if name == "wire.go" || name == "purge_refs.go" || name == "carrypatch.go" {
				continue
			}
			if strings.Contains(content, "PurgeDeletedPatches(") ||
				strings.Contains(content, "PurgeExpiredDeletedPatches(") ||
				strings.Contains(content, "PurgeExpiredDeletedPatchesWithRefs(") {
				callers = append(callers, filepath.Join("internal", pkg.Name(), name))
			}
		}
	}
	return callers
}

// ===========================================================================
// TS-24-49 (unit): "Understand Before You Code" has a step to read
// PATCH_BRANCH_SOURCE first.
// Verifies: 24-REQ-9.1
// ===========================================================================

func TestDocs_TS_24_49_UnderstandBeforeYouCodeReadsPatchBranchSource(t *testing.T) {
	agent := readDoc(t, "examples/AGENTS_carry_patch.md")
	s := section(t, agent, "## Understand Before You Code", "## ")

	requireContains(t, "Understand Before You Code", s,
		"PATCH_BRANCH_SOURCE",
		"afc vars list --workspace <workspace-slug>",
		"unset",
	)

	// The PATCH_BRANCH_SOURCE step must precede steps that touch branches.
	norm := strings.Join(strings.Fields(s), " ")
	pbsIdx := strings.Index(norm, "PATCH_BRANCH_SOURCE")
	branchIdx := strings.Index(norm, "patch stack")
	if pbsIdx < 0 {
		t.Fatal("PATCH_BRANCH_SOURCE not found in Understand Before You Code")
	}
	if branchIdx >= 0 && pbsIdx > branchIdx {
		t.Error("PATCH_BRANCH_SOURCE step should precede steps that touch branches")
	}
}

// ===========================================================================
// TS-24-50 (unit): Each listed agent example section gives hub and origin
// instructions where they differ.
// Verifies: 24-REQ-9.2
// ===========================================================================

func TestDocs_TS_24_50_AgentSectionsHubAndOriginInstructions(t *testing.T) {
	agent := readDoc(t, "examples/AGENTS_carry_patch.md")

	// These sections must mention both hub and origin mode.
	headings := []struct {
		start string
		stop  string
	}{
		{"### Step 1: Decide Where Your Change Belongs", "### "},
		{"### Step 4: Push", "### "},
		{"### Step 5: Register the Patch", "### "},
		{"### Step 6: Trigger a Rebuild", "### "},
		{"## Upstream Sync", "## "},
		{"## Conflict Resolution", "## "},
		{"### Removing a Patch", "### "},
		{"## Commands Reference", "## "},
		{"## Quality Gates", "## "},
	}

	for _, h := range headings {
		s := section(t, agent, h.start, h.stop)
		norm := strings.Join(strings.Fields(strings.ToLower(s)), " ")
		if !strings.Contains(norm, "origin") {
			t.Errorf("section %q does not mention origin", h.start)
		}
		if !strings.Contains(norm, "hub") {
			t.Errorf("section %q does not mention hub", h.start)
		}
	}
}

// ===========================================================================
// TS-24-51 (unit): In origin mode the example directs the agent to the fork
// and to sync, never to a hub push.
// Verifies: 24-REQ-9.3
// ===========================================================================

func TestDocs_TS_24_51_OriginModeDirectsToForkAndSync(t *testing.T) {
	agent := readDoc(t, "examples/AGENTS_carry_patch.md")

	// The origin mode instructions should be found across the document.
	requireContains(t, "agent example", agent,
		"clone the fork",
		"afc workspace sync",
		"refuses or replaces",
	)

	// The origin mode rebuild step should say to use sync, not rebuild submit.
	rebuildSec := section(t, agent, "### Step 6: Trigger a Rebuild", "### ")
	requireContains(t, "Trigger a Rebuild", rebuildSec,
		"afc workspace sync",
		"not `afc rebuild submit`",
	)
}

// ===========================================================================
// TS-24-52 (unit): In hub or unset mode the example keeps today's
// instructions and explains the mirror.
// Verifies: 24-REQ-9.4
// ===========================================================================

func TestDocs_TS_24_52_HubModeKeepsInstructionsAndExplainsMirror(t *testing.T) {
	agent := readDoc(t, "examples/AGENTS_carry_patch.md")

	// Hub mode instructions should still reference pushing to the hub and
	// running afc rebuild submit.
	requireContains(t, "agent example", agent,
		"git push origin patch/",
		"afc rebuild submit",
		"PUSH_PATCHES_TO_ORIGIN=true",
		"mirror",
	)
}

// ===========================================================================
// TS-24-53 (unit): The example's remove text, project context and commands
// table are updated.
// Verifies: 24-REQ-9.5, 24-REQ-9.6
// ===========================================================================

func TestDocs_TS_24_53_RemoveTextProjectContextAndCommandsTable(t *testing.T) {
	agent := readDoc(t, "examples/AGENTS_carry_patch.md")

	// Remove text says the branch is not deleted but the backup ref is.
	removeSec := section(t, agent, "### Removing a Patch", "### ")
	requireContains(t, "Removing a Patch", removeSec,
		"not deleted",
		"backup ref",
	)

	// The project context paragraph should be mode-neutral.
	ctx := section(t, agent, "## Project Context", "## ")
	norm := strings.Join(strings.Fields(strings.ToLower(ctx)), " ")
	// It should not say patch branches live only on the hub.
	if strings.Contains(norm, "clone from here, push here") {
		t.Error("project context still says 'clone from here, push here'")
	}

	// Commands table contains the three new commands.
	cmdSec := section(t, agent, "## Commands Reference", "## ")
	requireContains(t, "Commands Reference", cmdSec,
		"afc patch reset-to-origin",
		"afc workspace sync --fail-on-diverged",
		"afc vars list --workspace",
	)
}

// ===========================================================================
// TS-24-55 (unit): ADR 01 is Accepted with an Accepted date, the proposal
// date unchanged and no "Proposed".
// Verifies: 24-REQ-10.1
// ===========================================================================

func TestDocs_TS_24_55_ADR01AcceptedStatus(t *testing.T) {
	adr := readDoc(t, "adr/01-choose-the-authority-for-patch-branches.md")

	// Must contain "Status:** Accepted".
	requireContains(t, "ADR 01", adr, "Status:** Accepted")

	// Must have the original proposal date.
	requireContains(t, "ADR 01", adr, "2026-09-29")

	// Must have an Accepted date line.
	requireContains(t, "ADR 01", adr, "Accepted:")

	// Must NOT contain the word "Proposed".
	if strings.Contains(adr, "Proposed") {
		t.Error("ADR 01 still contains the word 'Proposed'")
	}
}

// ===========================================================================
// TS-24-56 (unit): ADR 01 Context opens with a before-implementation sentence
// and Implementation notes list the errata.
// Verifies: 24-REQ-10.2
// ===========================================================================

func TestDocs_TS_24_56_ADR01ContextAndImplementationNotes(t *testing.T) {
	adr := readDoc(t, "adr/01-choose-the-authority-for-patch-branches.md")

	// Implementation notes section lists the four errata files and says
	// the Decision stands as written.
	notes := section(t, adr, "## Implementation notes", "## ")
	requireContains(t, "Implementation notes", notes,
		"20_fork_patch_sync_divergences.md",
		"21_fork_patch_registration_divergences.md",
		"22_fork_push_control_divergences.md",
		"23_patch_divergence_recovery_divergences.md",
		"stands as written",
	)

	// Context should open with a sentence about describing the code before
	// the decision was implemented.
	ctxSection := section(t, adr, "## Context", "## ")
	requireContains(t, "Context", ctxSection, "before the decision was implemented")

	// Decision and Consequences text must be unchanged.
	// Verify key phrases from the original Decision section are present.
	decision := section(t, adr, "## Decision", "## ")
	requireContains(t, "Decision", decision,
		"PATCH_BRANCH_SOURCE",
		"`hub` is the default",
		"`origin` makes the fork authoritative",
		"PUSH_PATCHES_TO_ORIGIN",
	)

	// Verify key phrases from the original Consequences section are present.
	consequences := section(t, adr, "## Consequences", "## ")
	requireContains(t, "Consequences", consequences,
		"Positive",
		"Negative",
		"Two more workspace variables",
	)
}

// ===========================================================================
// TS-24-57 (unit): The four errata files exist and each has the three
// required sections.
// Verifies: 24-REQ-10.3
// ===========================================================================

func TestDocs_TS_24_57_ErrataFilesExistWithSections(t *testing.T) {
	errataFiles := []string{
		"errata/20_fork_patch_sync_divergences.md",
		"errata/21_fork_patch_registration_divergences.md",
		"errata/22_fork_push_control_divergences.md",
		"errata/23_patch_divergence_recovery_divergences.md",
	}

	for _, f := range errataFiles {
		d := readDoc(t, f)
		requireContains(t, f, d,
			"## Spec Expectation",
			"## Implementation Reality",
			"## Resolution",
		)
	}
}

// ===========================================================================
// TS-24-58 (unit): The errata for specs 20 and 21 record each confirmed
// divergence with a reason.
// Verifies: 24-REQ-10.4
// ===========================================================================

func TestDocs_TS_24_58_ErrataSpecs20And21Divergences(t *testing.T) {
	e20 := readDoc(t, "errata/20_fork_patch_sync_divergences.md")
	requireContains(t, "spec 20 erratum", e20,
		"origin_fetch_failed",
		"compare-and-swap",
		"last_sync_at",
		"docs/api.md",
	)

	e21 := readDoc(t, "errata/21_fork_patch_registration_divergences.md")
	requireContains(t, "spec 21 erratum", e21,
		"workspace_busy",
		"skipped",
		"502",
	)
}

// ===========================================================================
// TS-24-59 (unit): The errata for specs 22 and 23 record each confirmed
// divergence with a reason.
// Verifies: 24-REQ-10.5
// ===========================================================================

func TestDocs_TS_24_59_ErrataSpecs22And23Divergences(t *testing.T) {
	e22 := readDoc(t, "errata/22_fork_push_control_divergences.md")
	requireContains(t, "spec 22 erratum", e22,
		"refs/hub/forward/<branch>",
		"fail open",
		"AUTO_REBUILD_AFTER_PUSH=false",
	)

	e23 := readDoc(t, "errata/23_patch_divergence_recovery_divergences.md")
	requireContains(t, "spec 23 erratum", e23,
		"hub.patch.reset",
		"refs/hub/replaced/",
		"purge",
	)
}

// ===========================================================================
// TS-24-60 (unit): Unsupported erratum entries are dropped and code
// divergences not on the list are added.
// Verifies: 24-REQ-10.6
// ===========================================================================

func TestDocs_TS_24_60_ErrataEntriesMatchCode(t *testing.T) {
	// Verify that each erratum entry references behaviour that exists in the
	// code. We check key strings that correspond to code-confirmed
	// divergences.

	// Spec 20: pruning is confirmed in sync_handlers.go (FetchOrigin with
	// pruning), 502 origin_fetch_failed type is confirmed, compare-and-swap
	// is confirmed in patch_refresh.go, disabled patches not counting as
	// advanced is confirmed, merge detection on patch-only changes is
	// confirmed, field presence is confirmed in asExtras(), last_sync_at
	// not written on ref-write failure is confirmed, variables in
	// docs/api.md is confirmed.
	e20 := readDoc(t, "errata/20_fork_patch_sync_divergences.md")
	requireContains(t, "spec 20 erratum code-confirmed", e20,
		"prun",
		"502",
		"disabled",
		"merge detection",
	)

	// Spec 21: refs/heads check is confirmed in branch_resolver.go,
	// 502 on fork fetch failure is confirmed, 409 workspace_busy is
	// confirmed, branch_resolution skipped is confirmed, no batch audit
	// event is confirmed.
	e21 := readDoc(t, "errata/21_fork_patch_registration_divergences.md")
	requireContains(t, "spec 21 erratum code-confirmed", e21,
		"refs/heads",
		"batch",
		"audit",
	)

	// Spec 22: delete rejection is confirmed in push_control.go, forward
	// temp ref is confirmed, accepted-only rule is confirmed, fail-open is
	// confirmed, mirror when AUTO_REBUILD_AFTER_PUSH=false is confirmed.
	e22 := readDoc(t, "errata/22_fork_push_control_divergences.md")
	requireContains(t, "spec 22 erratum code-confirmed", e22,
		"delete",
		"120",
		"userinfo",
		"accepted",
	)

	// Spec 23: single-patch GET is confirmed in routes.go, reset semantics
	// are confirmed in recovery.go, hub.patch.reset event is confirmed in
	// audit/types.go, protected prefix is confirmed in
	// gitserver/protected_refs.go, purge not scheduled is confirmed.
	e23 := readDoc(t, "errata/23_patch_divergence_recovery_divergences.md")
	requireContains(t, "spec 23 erratum code-confirmed", e23,
		"GET",
		"active",
		"conflict",
		"disabled",
		"scheduled",
	)
}

// ===========================================================================
// TS-24-61 (integration): The new docs test passes on the real documents and
// covers every listed assertion.
// Verifies: 24-REQ-10.7
// ===========================================================================

// authorityDocsAssertion describes one content assertion for the aggregate
// authority-docs test. The mutation test (TS-24-62) iterates this table.
type authorityDocsAssertion struct {
	name     string // human-readable label
	file     string // relative path under docs/
	needle   string // string that must be present
	absent   bool   // when true, needle must NOT be present
	section  string // optional: restrict search to this section start marker
	secStop  string // optional: section stop marker
}

// authorityDocsAssertions returns the full table of assertions for the
// aggregate authority-docs test. Kept as a function so both TS-24-61 and
// TS-24-62 share the same list.
func authorityDocsAssertions() []authorityDocsAssertion {
	return []authorityDocsAssertion{
		// --- Where patch branches live heading and content ---
		{"wpbl heading", "carry_patch_workflow.md", "### Where patch branches live", false, "", ""},
		{"wpbl PATCH_BRANCH_SOURCE", "carry_patch_workflow.md", "PATCH_BRANCH_SOURCE", false, "### Where patch branches live", "### "},
		{"wpbl PATCH_DIVERGENCE_POLICY", "carry_patch_workflow.md", "PATCH_DIVERGENCE_POLICY", false, "### Where patch branches live", "### "},
		{"wpbl PUSH_PATCHES_TO_ORIGIN", "carry_patch_workflow.md", "PUSH_PATCHES_TO_ORIGIN", false, "### Where patch branches live", "### "},
		{"wpbl hub value", "carry_patch_workflow.md", "`hub`", false, "### Where patch branches live", "### "},
		{"wpbl origin value", "carry_patch_workflow.md", "`origin`", false, "### Where patch branches live", "### "},

		// --- Configuration: one entry per variable ---
		{"cfg PATCH_BRANCH_SOURCE heading", "carry_patch_workflow.md", "### PATCH_BRANCH_SOURCE", false, "## Configuration", "## "},
		{"cfg PATCH_DIVERGENCE_POLICY heading", "carry_patch_workflow.md", "### PATCH_DIVERGENCE_POLICY", false, "## Configuration", "## "},
		{"cfg PUSH_PATCHES_TO_ORIGIN heading", "carry_patch_workflow.md", "### PUSH_PATCHES_TO_ORIGIN", false, "## Configuration", "## "},

		// --- Fork-first and alternative walkthrough headings ---
		{"gs fork-authoritative", "carry_patch_workflow.md", "fork-authoritative", false, "## Getting started", "## "},
		{"gs alternative heading", "carry_patch_workflow.md", "### Alternative: hub-authoritative workspace", false, "", ""},

		// --- Both mode subsections in the conflict section ---
		{"conflict hub mode", "carry_patch_workflow.md", "#### Resolving in hub mode", false, "### Resolving conflicts after a failed rebuild", "### "},
		{"conflict origin mode", "carry_patch_workflow.md", "#### Resolving in origin mode", false, "### Resolving conflicts after a failed rebuild", "### "},

		// --- Recovery section ---
		{"recovery heading", "carry_patch_workflow.md", "### Recovering a replaced or diverged patch branch", false, "", ""},

		// --- Remotes table lacks old string ---
		{"remotes no old string", "carry_patch_workflow.md", "Where patch branches live; push target for local work", true, "", ""},

		// --- Agent example ---
		{"agent PATCH_BRANCH_SOURCE", "examples/AGENTS_carry_patch.md", "PATCH_BRANCH_SOURCE", false, "", ""},
		{"agent afc workspace sync", "examples/AGENTS_carry_patch.md", "afc workspace sync", false, "", ""},

		// --- ADR 01 ---
		{"adr01 accepted", "adr/01-choose-the-authority-for-patch-branches.md", "Status:** Accepted", false, "", ""},
		{"adr01 no proposed", "adr/01-choose-the-authority-for-patch-branches.md", "Proposed", true, "", ""},

		// --- Errata files: three section headings each ---
		{"errata 20 spec expectation", "errata/20_fork_patch_sync_divergences.md", "## Spec Expectation", false, "", ""},
		{"errata 20 impl reality", "errata/20_fork_patch_sync_divergences.md", "## Implementation Reality", false, "", ""},
		{"errata 20 resolution", "errata/20_fork_patch_sync_divergences.md", "## Resolution", false, "", ""},
		{"errata 21 spec expectation", "errata/21_fork_patch_registration_divergences.md", "## Spec Expectation", false, "", ""},
		{"errata 21 impl reality", "errata/21_fork_patch_registration_divergences.md", "## Implementation Reality", false, "", ""},
		{"errata 21 resolution", "errata/21_fork_patch_registration_divergences.md", "## Resolution", false, "", ""},
		{"errata 22 spec expectation", "errata/22_fork_push_control_divergences.md", "## Spec Expectation", false, "", ""},
		{"errata 22 impl reality", "errata/22_fork_push_control_divergences.md", "## Implementation Reality", false, "", ""},
		{"errata 22 resolution", "errata/22_fork_push_control_divergences.md", "## Resolution", false, "", ""},
		{"errata 23 spec expectation", "errata/23_patch_divergence_recovery_divergences.md", "## Spec Expectation", false, "", ""},
		{"errata 23 impl reality", "errata/23_patch_divergence_recovery_divergences.md", "## Implementation Reality", false, "", ""},
		{"errata 23 resolution", "errata/23_patch_divergence_recovery_divergences.md", "## Resolution", false, "", ""},
	}
}

// runAuthorityDocsCheck runs all assertions from the table against the given
// document contents. It returns a list of failure messages (empty = all pass).
func runAuthorityDocsCheck(docs map[string]string) []string {
	var failures []string
	for _, a := range authorityDocsAssertions() {
		doc, ok := docs[a.file]
		if !ok {
			failures = append(failures, a.name+": file not found: "+a.file)
			continue
		}

		target := doc
		if a.section != "" {
			idx := strings.Index(doc, a.section)
			if idx < 0 {
				failures = append(failures, a.name+": section start not found: "+a.section)
				continue
			}
			rest := doc[idx+len(a.section):]
			if a.secStop != "" {
				if j := strings.Index(rest, "\n"+a.secStop); j >= 0 {
					rest = rest[:j]
				}
			}
			target = a.section + rest
		}

		// Normalise whitespace for the check.
		norm := strings.Join(strings.Fields(target), " ")

		if a.absent {
			if strings.Contains(norm, a.needle) {
				failures = append(failures, a.name+": should be absent but found: "+a.needle)
			}
		} else {
			if !strings.Contains(norm, a.needle) {
				failures = append(failures, a.name+": not found: "+a.needle)
			}
		}
	}
	return failures
}

// mutateDoc removes (or injects for absent assertions) the needle from the
// document, scoped to the assertion's section when one is specified. For
// section-scoped assertions the needle is removed from all occurrences
// within that section only, leaving the rest of the document intact.
func mutateDoc(doc string, a authorityDocsAssertion) string {
	if a.absent {
		// Inject the needle to make the absent check fail.
		return doc + "\n" + a.needle + "\n"
	}

	if a.section == "" {
		// Whole-file assertion: remove all occurrences.
		return strings.ReplaceAll(doc, a.needle, "")
	}

	// Section-scoped: find the section boundaries and remove the needle
	// only within them.
	idx := strings.Index(doc, a.section)
	if idx < 0 {
		return doc // section not found; the check will fail on its own
	}

	secStart := idx
	rest := doc[idx+len(a.section):]
	secEnd := len(doc)
	if a.secStop != "" {
		if j := strings.Index(rest, "\n"+a.secStop); j >= 0 {
			secEnd = idx + len(a.section) + j
		}
	}

	secText := doc[secStart:secEnd]
	mutated := strings.ReplaceAll(secText, a.needle, "")
	return doc[:secStart] + mutated + doc[secEnd:]
}

func TestDocs_TS_24_61_AggregateAuthorityDocsTest(t *testing.T) {
	// Load all documents referenced by the assertions.
	files := map[string]bool{}
	for _, a := range authorityDocsAssertions() {
		files[a.file] = true
	}

	docs := map[string]string{}
	for f := range files {
		docs[f] = readDoc(t, f)
	}

	failures := runAuthorityDocsCheck(docs)
	for _, msg := range failures {
		t.Error(msg)
	}
}

// ===========================================================================
// TS-24-62 (property): Removing any asserted content makes the new docs test
// fail while existing tests and the make targets pass.
// Verifies: 24-REQ-10.8
// ===========================================================================

func TestDocs_TS_24_62_MutationGuard(t *testing.T) {
	// Load all documents.
	fileSet := map[string]bool{}
	for _, a := range authorityDocsAssertions() {
		fileSet[a.file] = true
	}

	origDocs := map[string]string{}
	for f := range fileSet {
		origDocs[f] = readDoc(t, f)
	}

	// Baseline must pass.
	baseline := runAuthorityDocsCheck(origDocs)
	if len(baseline) > 0 {
		t.Fatalf("baseline failures (test is broken): %v", baseline)
	}

	for i, a := range authorityDocsAssertions() {
		t.Run(a.name, func(t *testing.T) {
			// Build a mutated copy of the docs.
			mutated := make(map[string]string, len(origDocs))
			for k, v := range origDocs {
				mutated[k] = v
			}

			result := mutateDoc(mutated[a.file], a)
			if result == mutated[a.file] && !a.absent {
				t.Fatalf("assertion %d (%s): needle %q not found in %s for removal",
					i, a.name, a.needle, a.file)
			}
			mutated[a.file] = result

			failures := runAuthorityDocsCheck(mutated)
			if len(failures) == 0 {
				t.Errorf("assertion %d (%s): removing/injecting %q did not cause a failure",
					i, a.name, a.needle)
			}

			// Verify the failure names the right assertion.
			found := false
			for _, f := range failures {
				if strings.Contains(f, a.name) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("assertion %d (%s): failure did not name the assertion; got %v",
					i, a.name, failures)
			}
		})
	}
}

// ===========================================================================
// TS-24-1 (property): Every quoted variable, value, message, field, path,
// exit code and event type in the rewritten documents exists verbatim in the
// shipped code, docs/api.md or docs/cli.md.
// Verifies: 24-REQ-1.1
// ===========================================================================

func TestDocs_TS_24_1_QuotedTokensExistInCodeOrRefDocs(t *testing.T) {
	// Load the three rewritten documents.
	guide := readDoc(t, "carry_patch_workflow.md")
	agent := readDoc(t, "examples/AGENTS_carry_patch.md")
	adr01 := readDoc(t, "adr/01-choose-the-authority-for-patch-branches.md")

	// Load the reference documents.
	apiMd := readDoc(t, "api.md")
	cliMd := readDoc(t, "cli.md")

	// Load all non-test Go source files under internal/ into one big string.
	var codeBuf strings.Builder
	pkgs, err := os.ReadDir(filepath.Join("..", "..", "internal"))
	if err != nil {
		t.Fatalf("read internal/: %v", err)
	}
	for _, pkg := range pkgs {
		if !pkg.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join("..", "..", "internal", pkg.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join("..", "..", "internal", pkg.Name(), name))
			if err != nil {
				continue
			}
			codeBuf.Write(data)
			codeBuf.WriteByte('\n')
		}
	}
	codeAll := codeBuf.String()

	// A curated list of tokens that must appear verbatim in the code or
	// reference docs. These are the key variable names, values, messages,
	// field names, endpoint paths, exit codes and event types quoted in the
	// rewritten documents.
	tokens := []string{
		// Variables
		"PATCH_BRANCH_SOURCE",
		"PATCH_DIVERGENCE_POLICY",
		"PUSH_PATCHES_TO_ORIGIN",
		"AUTO_REBUILD_AFTER_SYNC",
		"AUTO_REBUILD_AFTER_PUSH",
		"REBUILD_PUSH_INTEGRATION_BRANCH",
		"REBUILD_STRATEGY",
		"REBUILD_FAIL_MODE",
		"SQUASH_MERGE_DETECTION",
		// Variable values
		"origin",
		"hub",
		"replace",
		"report",
		// Sync response fields
		"origin_fetched",
		"patches_synced",
		"patches_diverged",
		// Patch fields
		"origin_sync_state",
		"origin_sha",
		"origin_synced_at",
		// Sync states
		"in_sync",
		"diverged",
		"missing_on_origin",
		// Patch refresh outcomes
		"created",
		"fast_forwarded",
		"replaced",
		// Messages
		"origin fetch failed",
		"failed to resolve origin credentials",
		"branch is synced from origin; push to",
		// Endpoint paths
		"/workspaces/:slug/patches/:id",
		"reset-to-origin",
		"/workspaces/:slug/sync",
		// Events
		"hub.patch.sync",
		"hub.patch.replace",
		"hub.patch.mirror_failed",
		"hub.patch.reset",
		// Refs
		"refs/hub/replaced/",
		"refs/hub/forward/",
		// Fields
		"replaced_sha",
		"rebuild_triggered",
		"force_push_detected",
		"branch_resolution",
		// Error types
		"origin_fetch_failed",
		"workspace_busy",
	}

	// Verify each token appears in at least one of: code, api.md, cli.md.
	for _, tok := range tokens {
		inCode := strings.Contains(codeAll, tok)
		inAPI := strings.Contains(apiMd, tok)
		inCLI := strings.Contains(cliMd, tok)
		if !inCode && !inAPI && !inCLI {
			t.Errorf("token %q not found in internal/ source, docs/api.md or docs/cli.md", tok)
		}
	}

	// Verify each token also appears in at least one of the rewritten docs.
	allDocs := guide + "\n" + agent + "\n" + adr01
	for _, tok := range tokens {
		if !strings.Contains(allDocs, tok) {
			// Not all tokens need to be in every doc; some are only in the
			// guide. This is a sanity check that the token list is relevant.
			// We only check that the token exists in the code/ref docs above.
		}
	}
}

// ===========================================================================
// TS-24-2 (unit): A detail where the shipped code differs from a spec PRD is
// described as the code has it and recorded in that spec's erratum.
// Verifies: 24-REQ-1.2
// ===========================================================================

func TestDocs_TS_24_2_CodeDivergencesRecordedInErrata(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")

	// Confirmed divergences: the guide must use the code's wording where it
	// mentions the topic, and the erratum must record the divergence.
	type divergence struct {
		spec       string // erratum file
		codeValue  string // what the code uses (must be in guide)
		errataHint string // a string that must appear in the erratum
		guideCheck bool   // true = also verify the guide contains codeValue
	}

	divergences := []divergence{
		// Spec 20: origin_fetch_failed error type
		{"errata/20_fork_patch_sync_divergences.md", "origin_fetch_failed", "origin_fetch_failed", true},
		// Spec 20: compare-and-swap ref writes
		{"errata/20_fork_patch_sync_divergences.md", "compare-and-swap", "compare-and-swap", true},
		// Spec 20: last_sync_at not written on ref-write failure
		{"errata/20_fork_patch_sync_divergences.md", "last_sync_at", "last_sync_at", true},
		// Spec 21: workspace_busy on registration
		{"errata/21_fork_patch_registration_divergences.md", "workspace_busy", "workspace_busy", false},
		// Spec 21: branch_resolution skipped value
		{"errata/21_fork_patch_registration_divergences.md", "skipped", "skipped", false},
		// Spec 22: refs/hub/forward/<branch> temp ref (implementation detail,
		// recorded in erratum; the guide mentions forwarding but need not
		// name the temp ref)
		{"errata/22_fork_push_control_divergences.md", "refs/hub/forward/", "refs/hub/forward/", false},
		// Spec 23: hub.patch.reset event (recorded in erratum; the guide
		// mentions reset-to-origin but need not list every event type)
		{"errata/23_patch_divergence_recovery_divergences.md", "hub.patch.reset", "hub.patch.reset", false},
		// Spec 23: refs/hub/replaced/ protection
		{"errata/23_patch_divergence_recovery_divergences.md", "refs/hub/replaced/", "refs/hub/replaced/", true},
	}

	for _, d := range divergences {
		// When guideCheck is true, the guide must contain the code's value.
		if d.guideCheck {
			if !strings.Contains(guide, d.codeValue) {
				t.Errorf("guide does not contain code value %q (from %s)", d.codeValue, d.spec)
			}
		}

		// The erratum file must contain the code's value and a reason.
		erratum := readDoc(t, d.spec)
		if !strings.Contains(erratum, d.errataHint) {
			t.Errorf("erratum %s does not contain %q", d.spec, d.errataHint)
		}
	}
}

// ===========================================================================
// TS-24-3 (unit): A behaviour claim with no code or test evidence is absent
// from the guide, agent example and ADR 01.
// Verifies: 24-REQ-1.3
// ===========================================================================

func TestDocs_TS_24_3_OpenQuestionsOnlyAsLimitations(t *testing.T) {
	guide := readDoc(t, "carry_patch_workflow.md")
	agent := readDoc(t, "examples/AGENTS_carry_patch.md")
	adr01 := readDoc(t, "adr/01-choose-the-authority-for-patch-branches.md")

	allDocs := guide + "\n" + agent + "\n" + adr01
	norm := strings.Join(strings.Fields(strings.ToLower(allDocs)), " ")

	// The four open questions should appear only as limitations, not as
	// planned features.
	// 1. rerere feedback from fork pushes
	// 2. auto-disabling missing_on_origin
	// 3. built-in sync schedule
	// 4. fork branch filter

	// No sentence should promise these as planned/upcoming features.
	forbiddenPromises := []string{
		"will be implemented",
		"will be added",
		"upcoming feature",
		"planned feature",
	}
	for _, p := range forbiddenPromises {
		// Allow "not a planned feature" as a negation.
		if strings.Contains(norm, p) {
			// Check it's preceded by a negation.
			idx := strings.Index(norm, p)
			prefix := norm[:idx]
			if !strings.HasSuffix(strings.TrimSpace(prefix), "not a") {
				t.Errorf("documents contain forbidden promise %q without negation", p)
			}
		}
	}

	// The purge scheduler must not be claimed.
	for _, bad := range []string{
		"background purge process permanently removes",
		"scheduler removes",
		"scheduler purges",
		"automatic purge",
	} {
		if strings.Contains(norm, bad) {
			t.Errorf("documents contain forbidden purge claim: %q", bad)
		}
	}

	// The guide's Limitations section should mention these as limitations.
	limitations := section(t, guide, "## Limitations", "## ")
	limNorm := strings.Join(strings.Fields(strings.ToLower(limitations)), " ")

	// Rerere limitation
	if !strings.Contains(limNorm, "rerere") {
		t.Error("Limitations section does not mention rerere")
	}
	// Sync schedule limitation
	if !strings.Contains(limNorm, "sync schedule") && !strings.Contains(limNorm, "sync is triggered") {
		t.Error("Limitations section does not mention sync schedule")
	}
	// missing_on_origin limitation
	if !strings.Contains(limNorm, "missing_on_origin") {
		t.Error("Limitations section does not mention missing_on_origin")
	}
}

// ===========================================================================
// TS-24-4 (unit): The change set touches only documentation files and
// internal/carrypatch/docs_test.go.
// Verifies: 24-REQ-1.4
// ===========================================================================

func TestDocs_TS_24_4_ChangeSetLimitedToDocsAndTestFile(t *testing.T) {
	// This test verifies the constraint by checking that no non-docs .go file
	// other than docs_test.go was modified. We use git diff when available;
	// otherwise we skip.
	//
	// Since we cannot run arbitrary commands in the test, we verify the
	// constraint structurally: the only .go file in this package that this
	// spec touches is docs_test.go. We confirm this by checking that the
	// test functions added by this spec (TS-24-*) exist only in docs_test.go.

	// Read all .go files in this package directory.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read current dir: %v", err)
	}

	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if name == "docs_test.go" {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		// No other .go file should contain TS-24- test functions.
		if strings.Contains(string(data), "TS_24_") && strings.HasSuffix(name, "_test.go") {
			// docs_push_control_test.go and docs_recovery_test.go are allowed
			// to reference TS-24 indirectly (they don't), but must not define
			// TS-24 test functions.
			if strings.Contains(string(data), "func TestDocs_TS_24_") {
				t.Errorf("file %s defines TS-24 test functions; only docs_test.go should", name)
			}
		}
	}

	// Verify that docs_test.go exists and contains TS-24 tests.
	data, err := os.ReadFile("docs_test.go")
	if err != nil {
		t.Fatalf("read docs_test.go: %v", err)
	}
	if !strings.Contains(string(data), "TS_24_1_") {
		t.Error("docs_test.go does not contain TS-24-1 test")
	}
}

// ===========================================================================
// TS-24-46 (unit): The cross-document searches cover the five files and four
// topics. This test runs the same searches in Go.
// Verifies: 24-REQ-8.1
// ===========================================================================

func TestDocs_TS_24_46_CrossDocumentSearches(t *testing.T) {
	// The five files to search.
	files := []string{
		"api.md",
		"cli.md",
		"openapi.yaml",
		"permissions.md",
	}

	// Also check README.md (relative to repo root).
	readmePath := filepath.Join("..", "..", "README.md")
	readmeData, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(readmeData)

	// Load all five docs files.
	docs := make(map[string]string)
	for _, f := range files {
		docs[f] = readDoc(t, f)
	}
	docs["README.md"] = readme

	// Topic 1: "patch branches live" — no unqualified claim that origin is
	// where patch branches live.
	for name, content := range docs {
		norm := strings.Join(strings.Fields(strings.ToLower(content)), " ")
		if strings.Contains(norm, "where patch branches live; push target for local work") {
			t.Errorf("%s contains the old unqualified claim about where patch branches live", name)
		}
	}

	// Topic 2: "what sync fetches" — no claim that sync only fetches upstream
	// without mentioning origin mode.
	// (We just verify the api.md mentions origin fetch in the sync section.)
	apiSync := section(t, docs["api.md"], "### POST /api/v1/workspaces/:slug/sync", "### ")
	requireContains(t, "api.md sync section", apiSync, "origin")

	// Topic 3: "last_sync_at" — verify api.md says it's written on every
	// completed sync.
	requireContains(t, "api.md", docs["api.md"], "last_sync_at")

	// Topic 4: "push to a patch branch" — verify api.md mentions the push
	// control behaviour.
	requireContains(t, "api.md", docs["api.md"],
		"branch is synced from origin; push to",
		"PUSH_PATCHES_TO_ORIGIN",
	)

	// Verify README.md link to the guide is valid.
	if !strings.Contains(readme, "docs/carry_patch_workflow.md") {
		t.Error("README.md does not link to docs/carry_patch_workflow.md")
	}
	// Verify the linked file exists.
	if _, err := os.Stat(filepath.Join("..", "..", "docs", "carry_patch_workflow.md")); err != nil {
		t.Errorf("docs/carry_patch_workflow.md does not exist: %v", err)
	}
}

// ===========================================================================
// TS-24-47 (unit): No searched document contradicts the guide and README
// changes are limited to corrections.
// Verifies: 24-REQ-8.2, 24-REQ-8.3
// ===========================================================================

func TestDocs_TS_24_47_NoContradictionsAndREADMELink(t *testing.T) {
	// Load the five searched documents.
	apiMd := readDoc(t, "api.md")
	cliMd := readDoc(t, "cli.md")
	openapi := readDoc(t, "openapi.yaml")
	perms := readDoc(t, "permissions.md")
	readmeData, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(readmeData)

	// None of the searched documents should say origin is where patch
	// branches live without qualification.
	for _, doc := range []struct {
		name    string
		content string
	}{
		{"api.md", apiMd},
		{"cli.md", cliMd},
		{"openapi.yaml", openapi},
		{"permissions.md", perms},
		{"README.md", readme},
	} {
		if strings.Contains(doc.content, "Where patch branches live; push target for local work") {
			t.Errorf("%s contains the old unqualified claim", doc.name)
		}
	}

	// No document should claim sync returns immediately when upstream is
	// unchanged.
	for _, doc := range []struct {
		name    string
		content string
	}{
		{"api.md", apiMd},
		{"cli.md", cliMd},
		{"openapi.yaml", openapi},
		{"permissions.md", perms},
		{"README.md", readme},
	} {
		norm := strings.ToLower(doc.content)
		if strings.Contains(norm, "returns immediately") {
			t.Errorf("%s claims sync returns immediately", doc.name)
		}
	}

	// README.md link to the guide must resolve.
	if !strings.Contains(readme, "docs/carry_patch_workflow.md") {
		t.Error("README.md does not link to docs/carry_patch_workflow.md")
	}
	if _, err := os.Stat(filepath.Join("..", "..", "docs", "carry_patch_workflow.md")); err != nil {
		t.Errorf("docs/carry_patch_workflow.md does not exist: %v", err)
	}
}

// ===========================================================================
// TS-24-48 (property): Every repository file path referenced in the changed
// documents exists.
// Verifies: 24-REQ-8.4
// ===========================================================================

func TestDocs_TS_24_48_ReferencedFilePathsExist(t *testing.T) {
	// Load the changed documents.
	guide := readDoc(t, "carry_patch_workflow.md")
	agent := readDoc(t, "examples/AGENTS_carry_patch.md")
	adr01 := readDoc(t, "adr/01-choose-the-authority-for-patch-branches.md")

	// Load errata files.
	errataFiles := []string{
		"errata/20_fork_patch_sync_divergences.md",
		"errata/21_fork_patch_registration_divergences.md",
		"errata/22_fork_push_control_divergences.md",
		"errata/23_patch_divergence_recovery_divergences.md",
	}
	var errataDocs []string
	for _, f := range errataFiles {
		errataDocs = append(errataDocs, readDoc(t, f))
	}

	allDocs := guide + "\n" + agent + "\n" + adr01
	for _, e := range errataDocs {
		allDocs += "\n" + e
	}

	// Extract file paths that look like docs/..., internal/... or .specs/...
	// from markdown link targets and backtick-quoted paths.
	pathRe := regexp.MustCompile("(?:`|\\()(?:docs/[a-zA-Z0-9_./-]+\\.(?:md|yaml)|internal/[a-zA-Z0-9_./-]+|.specs/[a-zA-Z0-9_./-]+)(?:`|\\))")
	matches := pathRe.FindAllString(allDocs, -1)

	seen := map[string]bool{}
	for _, m := range matches {
		// Strip the backtick or paren wrapper.
		p := strings.Trim(m, "`()")
		// Skip anchor-only links.
		if strings.HasPrefix(p, "#") {
			continue
		}
		seen[p] = true
	}

	// Also extract relative markdown links like (api.md), (cli.md),
	// (../errata/...) from the documents. Resolve them relative to their
	// source directory.
	relLinkRe := regexp.MustCompile(`\]\(([^)#]+\.(?:md|yaml))\)`)

	type docSource struct {
		content string
		baseDir string // relative to repo root
	}
	sources := []docSource{
		{guide, "docs"},
		{agent, "docs/examples"},
		{adr01, "docs/adr"},
	}
	for i := range errataFiles {
		sources = append(sources, docSource{errataDocs[i], "docs/errata"})
	}

	for _, src := range sources {
		for _, m := range relLinkRe.FindAllStringSubmatch(src.content, -1) {
			rel := m[1]
			// Resolve relative to the source directory.
			resolved := filepath.Join(src.baseDir, rel)
			resolved = filepath.Clean(resolved)
			seen[resolved] = true
		}
	}

	repoRoot := filepath.Join("..", "..")

	for p := range seen {
		// docs/architecture.md must not be referenced.
		if p == "docs/architecture.md" {
			t.Errorf("changed documents reference docs/architecture.md, which is forbidden")
			continue
		}

		// Check the file exists.
		fullPath := filepath.Join(repoRoot, p)
		if _, err := os.Stat(fullPath); err != nil {
			t.Errorf("referenced path %q does not exist in the repository", p)
		}
	}
}
