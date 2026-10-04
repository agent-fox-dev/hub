package carrypatch

import (
	"strings"
	"testing"
)

// ===========================================================================
// TS-22-46 (unit): docs/api.md documents the variable, the three push
// behaviours, per-ref messages, accepted-refs-only rule and mirror_failed
// event.
// Verifies: 22-REQ-9.2
// ===========================================================================

func TestDocs_TS_22_46_APIMdDocumentsPushControl(t *testing.T) {
	api := readDoc(t, "api.md")

	// Workspace Variables Reference lists PUSH_PATCHES_TO_ORIGIN
	varsSection := section(t, api, "## Workspace Variables Reference", "## ")
	requireContains(t, "api.md Workspace Variables Reference", varsSection,
		"PUSH_PATCHES_TO_ORIGIN",
	)

	// Git push section mentions the three behaviours and per-ref messages
	requireContains(t, "api.md", api,
		"branch is synced from origin; push to",
		"origin rejected push:",
		"failed to resolve origin credentials",
	)

	// Accepted refs only rule
	requireContains(t, "api.md", api,
		"accepted",
	)

	// hub.patch.mirror_failed event is documented
	requireContains(t, "api.md", api,
		"hub.patch.mirror_failed",
	)
}

// ===========================================================================
// TS-22-47 (unit): docs/openapi.yaml and docs/architecture.md describe the
// variable, rejections and pre-receive hook.
// Verifies: 22-REQ-9.3
// ===========================================================================

func TestDocs_TS_22_47_OpenAPIAndArchDescribePushControl(t *testing.T) {
	openapi := readDoc(t, "openapi.yaml")
	arch := readDoc(t, "architecture.md")

	// openapi mentions PUSH_PATCHES_TO_ORIGIN and the rejections in the
	// receive-pack route description
	requireContains(t, "openapi.yaml", openapi,
		"PUSH_PATCHES_TO_ORIGIN",
	)

	// architecture.md git server section mentions the pre-receive hook
	requireContains(t, "architecture.md", arch,
		"pre-receive",
	)
}

// ===========================================================================
// TS-22-48 (unit): carry_patch_workflow.md gets the variable entry and the
// updated note, while configuration.md and cli.md are unchanged.
// Verifies: 22-REQ-9.4
// ===========================================================================

func TestDocs_TS_22_48_WorkflowDocsUpdated(t *testing.T) {
	workflow := readDoc(t, "carry_patch_workflow.md")

	// Configuration section lists PUSH_PATCHES_TO_ORIGIN
	configSection := section(t, workflow, "## Configuration", "## ")
	requireContains(t, "carry_patch_workflow.md Configuration", configSection,
		"PUSH_PATCHES_TO_ORIGIN",
	)

	// The spec 20 note describes rejection, forwarding and mirroring
	// (the old "Until fork_push_control ships" note should be replaced)
	requireContains(t, "carry_patch_workflow.md", workflow,
		"reject",
		"forward",
		"mirror",
	)

	// configuration.md is unchanged (does not mention PUSH_PATCHES_TO_ORIGIN)
	cfg := readDoc(t, "configuration.md")
	if strings.Contains(cfg, "PUSH_PATCHES_TO_ORIGIN") {
		t.Error("configuration.md should not mention PUSH_PATCHES_TO_ORIGIN")
	}

	// cli.md is unchanged (does not mention PUSH_PATCHES_TO_ORIGIN)
	cliDoc := readDoc(t, "cli.md")
	if strings.Contains(cliDoc, "PUSH_PATCHES_TO_ORIGIN") {
		t.Error("cli.md should not mention PUSH_PATCHES_TO_ORIGIN")
	}
}
