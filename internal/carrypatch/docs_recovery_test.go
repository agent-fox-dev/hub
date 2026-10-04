package carrypatch

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ===========================================================================
// TS-23-63 (unit): Documentation describes the new endpoints, removal cleanup,
// push rejection and event, and configuration.md is unchanged.
// Verifies: 23-REQ-9.5
// ===========================================================================

func TestDocs_TS_23_63_RecoveryDocumentation(t *testing.T) {
	// --- docs/api.md ---
	api := readDoc(t, "api.md")

	// GET and reset-to-origin sections
	requireContains(t, "api.md", api,
		"GET /api/v1/workspaces/:slug/patches/:id",
		"reset-to-origin",
		"replaced_sha",
	)

	// Recovery command
	requireContains(t, "api.md", api,
		"git fetch <hub_url> refs/hub/replaced/<branch>",
	)

	// Removal note (backup ref deleted on remove)
	// The DELETE section should mention backup ref cleanup
	deleteSec := section(t, api, "### DELETE /api/v1/workspaces/:slug/patches/:id", "### ")
	requireContains(t, "api.md DELETE section", deleteSec, "backup")

	// Push rejection message
	requireContains(t, "api.md", api,
		"refs/hub/replaced/ is maintained by the hub and cannot be pushed to",
	)

	// Permissions table lines for patches:read and patches:write
	// patches:read should list the GET single-patch endpoint
	permTable := section(t, api, "## Permission Scopes", "### ")
	requireContains(t, "api.md permissions table", permTable,
		"GET /api/v1/workspaces/:slug/patches/:id",
	)

	// hub.patch.reset event
	requireContains(t, "api.md", api, "hub.patch.reset")

	// --- docs/openapi.yaml ---
	raw := readDoc(t, "openapi.yaml")

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("docs/openapi.yaml is not valid YAML: %v", err)
	}

	// get operation on /api/v1/workspaces/{slug}/patches/{id}
	patchIdPath, ok := navigateMap(parsed, "paths", "/api/v1/workspaces/{slug}/patches/{id}")
	if !ok {
		t.Fatal("openapi.yaml has no /api/v1/workspaces/{slug}/patches/{id} path")
	}
	if _, ok := patchIdPath["get"]; !ok {
		t.Error("openapi.yaml has no GET operation on /api/v1/workspaces/{slug}/patches/{id}")
	}

	// /reset-to-origin path
	resetPath, ok := navigateMap(parsed, "paths", "/api/v1/workspaces/{slug}/patches/{id}/reset-to-origin")
	if !ok {
		t.Fatal("openapi.yaml has no /api/v1/workspaces/{slug}/patches/{id}/reset-to-origin path")
	}
	resetPost, ok := navigateMap(resetPath, "post")
	if !ok {
		t.Fatal("openapi.yaml has no POST operation on reset-to-origin path")
	}

	// Check reset-to-origin responses: 200, 400, 404, 409, 502
	resetResponses, ok := navigateMap(resetPost, "responses")
	if !ok {
		t.Fatal("reset-to-origin POST has no responses")
	}
	for _, code := range []string{"200", "400", "404", "409", "502"} {
		if _, ok := resetResponses[code]; !ok {
			t.Errorf("reset-to-origin POST has no %s response", code)
		}
	}

	// replaced_sha, rebuild_triggered, rebuild_job_id properties
	requireContains(t, "openapi.yaml", raw, "replaced_sha")
	requireContains(t, "openapi.yaml", raw, "rebuild_triggered")
	requireContains(t, "openapi.yaml", raw, "rebuild_job_id")

	// --- docs/cli.md ---
	cliDoc := readDoc(t, "cli.md")
	requireContains(t, "cli.md", cliDoc, "reset-to-origin")

	// --- docs/permissions.md ---
	perms := readDoc(t, "permissions.md")
	// patches:read should mention the GET single-patch endpoint
	patchesReadSec := section(t, perms, "### patches:read", "### ")
	requireContains(t, "permissions.md patches:read", patchesReadSec,
		"GET /api/v1/workspaces/:slug/patches/:id",
	)
	// patches:write should mention reset-to-origin
	patchesWriteSec := section(t, perms, "### patches:write", "### ")
	requireContains(t, "permissions.md patches:write", patchesWriteSec,
		"reset-to-origin",
	)

	// --- docs/carry_patch_workflow.md ---
	wf := readDoc(t, "carry_patch_workflow.md")
	// Two rows in the patch-endpoint table: GET single patch and reset-to-origin
	requireContains(t, "carry_patch_workflow.md", wf,
		"reset-to-origin",
	)
	// The endpoint table should have a GET row for single patch
	patchEndpoints := section(t, wf, "### Patch endpoints", "### ")
	requireContains(t, "carry_patch_workflow.md patch endpoints", patchEndpoints,
		"/workspaces/:slug/patches/:id",
		"reset-to-origin",
	)

	// --- docs/architecture.md ---
	arch := readDoc(t, "architecture.md")
	// If the hook list names the others (PreReceiveHook, PostPushHook), it should
	// also name the recovery hook
	if strings.Contains(arch, "PreReceiveHook") || strings.Contains(arch, "PostPushHook") {
		requireContains(t, "architecture.md", arch, "RecoveryHook")
	}

	// --- docs/configuration.md ---
	cfg := readDoc(t, "configuration.md")
	if strings.Contains(cfg, "reset-to-origin") {
		t.Error("docs/configuration.md should not mention reset-to-origin")
	}
	if strings.Contains(cfg, "replaced_sha") {
		t.Error("docs/configuration.md should not mention replaced_sha")
	}
}
