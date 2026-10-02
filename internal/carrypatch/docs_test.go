package carrypatch

import (
	"os"
	"os/exec"
	"path/filepath"
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
		"fork_push_control",
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
// TS-21-42 (unit): docs/carry_patch_workflow.md is unmodified by this change.
// Verifies: 21-REQ-10.5
// ===========================================================================

func TestDocs_TS_21_42_CarryPatchWorkflowUnmodified(t *testing.T) {
	// Use git diff to verify the file is unchanged.
	out, err := exec.Command("git", "diff", "--quiet", "--", "docs/carry_patch_workflow.md").CombinedOutput()
	if err != nil {
		t.Fatalf("docs/carry_patch_workflow.md has been modified: %s", string(out))
	}
}
