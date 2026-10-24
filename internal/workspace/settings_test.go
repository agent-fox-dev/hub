package workspace

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

func TestSettings_DefaultsAndApply(t *testing.T) {
	d := DefaultSettings()
	if d.PatchBranchSource != "hub" || d.PatchDivergencePolicy != "replace" || !d.AutoRebuildAfterPush ||
		!d.AutoRebuildAfterSync || d.PushPatchesToOrigin || d.RebuildStrategy != "rebase" ||
		d.RebuildFailMode != "fail_fast" || d.RebuildPushIntegrationBranch || d.SquashMergeDetection != "both" ||
		d.ForgeAPIURL != "" || d.UpstreamForgeAPIURL != "" {
		t.Errorf("DefaultSettings() = %+v", d)
	}

	// A given key is taken, a null key returns to its default, an absent key keeps base.
	base := d
	base.PatchBranchSource = "origin"
	base.RebuildStrategy = "merge"
	got, err := applySettings(base, json.RawMessage(`{"push_patches_to_origin": true, "rebuild_strategy": null, "forge_api_url": "https://api.example.test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.PatchBranchSource != "origin" || !got.PushPatchesToOrigin || got.RebuildStrategy != "rebase" || got.ForgeAPIURL != "https://api.example.test" {
		t.Errorf("applySettings = %+v", got)
	}

	// Refusals.
	for body, want := range map[string]string{
		`[]`:                                   "settings must be an object",
		`{"colour": "blue"}`:                   `unknown setting "colour"`,
		`{"push_patches_to_origin": "yes"}`:    "setting push_patches_to_origin must be true or false",
		`{"patch_branch_source": "github"}`:    `setting patch_branch_source must be one of "hub", "origin"`,
		`{"forge_api_url": "http://insecure"}`: "setting forge_api_url must be an absolute https:// URL",
		`{"forge_api_url": 7}`:                 "setting forge_api_url must be a string",
	} {
		if _, err := applySettings(d, json.RawMessage(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("applySettings(%s) error = %v; want %q", body, err, want)
		}
	}

	// The stored document round-trips, and an old empty one gives the defaults.
	if back := decodeSettings(encodeSettings(got)); back != got {
		t.Errorf("round trip = %+v; want %+v", back, got)
	}
	if decodeSettings("{}") != d || decodeSettings("") != d {
		t.Error("an empty document should give the defaults")
	}
}

func TestSettingsReader(t *testing.T) {
	env := newTestEnv(t)
	env.seedWorkspace(t, &Workspace{Slug: "ws", GitURL: "https://github.com/a/b", OwnerID: "alice-id", Status: "active",
		Settings: func() Settings {
			s := DefaultSettings()
			s.PatchBranchSource = "origin"
			s.PushPatchesToOrigin = true
			s.ForgeAPIURL = "https://api.example.test"
			return s
		}()})
	read := SettingsReader(env.db)
	for key, want := range map[string]string{
		"PATCH_BRANCH_SOURCE":     "origin",
		"push_patches_to_origin":  "true",
		"AUTO_REBUILD_AFTER_PUSH": "true",
		"REBUILD_STRATEGY":        "rebase",
		"FORGE_API_URL":           "https://api.example.test",
	} {
		got, err := read("workspace", "ws", key)
		if err != nil || got != want {
			t.Errorf("read(%s) = %q, %v; want %q", key, got, err, want)
		}
	}
	var notFound *secrets.NotFoundError
	for _, c := range []struct{ owner, slug, key string }{
		{"workspace", "ws", "UPSTREAM_FORGE_API_URL"}, // empty URL is not set
		{"workspace", "ws", "CHECK_COMMAND"},          // not a setting
		{"workspace", "nope", "PATCH_BRANCH_SOURCE"},  // no such workspace
		{"user", "alice-id", "PATCH_BRANCH_SOURCE"},   // not a workspace
	} {
		if _, err := read(c.owner, c.slug, c.key); !errors.As(err, &notFound) {
			t.Errorf("read(%s, %s, %s) error = %v; want *secrets.NotFoundError", c.owner, c.slug, c.key, err)
		}
	}
}

func TestSettings_CreateAndUpdateThroughAPI(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-id")
	origValidate := validateCredentialsFn
	validateCredentialsFn = nil
	t.Cleanup(func() { validateCredentialsFn = origValidate })
	origClone := cloneFn
	cloneFn = nil
	t.Cleanup(func() { cloneFn = origClone })

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces",
		`{"slug":"with-settings","git_url":"https://github.com/a/b","settings":{"patch_branch_source":"origin","auto_rebuild_after_push":false}}`, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	body := decodeObject(t, rec)
	settings := body["settings"].(map[string]any)
	if settings["patch_branch_source"] != "origin" || settings["auto_rebuild_after_push"] != false || settings["rebuild_strategy"] != "rebase" {
		t.Errorf("created settings = %v", settings)
	}

	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces",
		`{"slug":"bad-settings","git_url":"https://github.com/a/b","settings":{"rebuild_strategy":"squash"}}`, auth)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "rebuild_strategy must be one of") {
		t.Errorf("create with a bad setting: %d %s; want 400", rec.Code, rec.Body)
	}

	// PATCH merges: one key changed, one reset, the rest kept.
	rec = env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/with-settings",
		`{"settings":{"rebuild_strategy":"merge","auto_rebuild_after_push":null}}`, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	settings = decodeObject(t, rec)["settings"].(map[string]any)
	if settings["rebuild_strategy"] != "merge" || settings["auto_rebuild_after_push"] != true || settings["patch_branch_source"] != "origin" {
		t.Errorf("patched settings = %v", settings)
	}
	if got, _ := SettingsReader(env.db)("workspace", "with-settings", "REBUILD_STRATEGY"); got != "merge" {
		t.Errorf("reader after patch = %q; want merge", got)
	}
	rec = env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/with-settings", `{"settings":{"nope":1}}`, auth)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("patch with an unknown setting: %d; want 400", rec.Code)
	}
}

func TestMigrateVariableSettings(t *testing.T) {
	env := newTestEnv(t)
	if err := secrets.InitSchema(env.db); err != nil {
		t.Fatal(err)
	}
	env.seedWorkspace(t, &Workspace{Slug: "old", GitURL: "https://github.com/a/b", OwnerID: "alice-id", Status: "active"})
	env.seedWorkspace(t, &Workspace{Slug: "plain", GitURL: "https://github.com/a/c", OwnerID: "alice-id", Status: "active"})
	store := secrets.NewStore(env.db)
	if _, err := store.CreateVariables("workspace", "old", []secrets.EntryInput{
		{Key: "PATCH_BRANCH_SOURCE", Value: "origin"},
		{Key: "AUTO_REBUILD_AFTER_SYNC", Value: "false"},
		{Key: "REBUILD_STRATEGY", Value: "squash"}, // not a value the setting takes: dropped
		{Key: "MY_OWN_VARIABLE", Value: "kept"},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := MigrateVariableSettings(env.db)
	if err != nil || n != 1 {
		t.Fatalf("MigrateVariableSettings = %d, %v; want 1", n, err)
	}
	ws, err := getWorkspaceBySlug(env.db, "old")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Settings.PatchBranchSource != "origin" || ws.Settings.AutoRebuildAfterSync || ws.Settings.RebuildStrategy != "rebase" {
		t.Errorf("migrated settings = %+v", ws.Settings)
	}
	vars, err := store.ListVariables("workspace", "old")
	if err != nil || len(vars) != 1 || vars[0].Key != "MY_OWN_VARIABLE" {
		t.Errorf("remaining variables = %v, %v; want only MY_OWN_VARIABLE", vars, err)
	}
	if n, err := MigrateVariableSettings(env.db); err != nil || n != 0 {
		t.Errorf("second run = %d, %v; want 0", n, err)
	}
}
