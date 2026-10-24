package workspace

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// TestSettings_Apply: a given key is taken, a null key returns to its
// default, an absent key keeps the base, null resets everything, and the
// refusals name the key and what it takes.
func TestSettings_Apply(t *testing.T) {
	d := DefaultSettings()
	if d.PatchBranchSource != "hub" || d.PatchDivergencePolicy != "replace" || !d.AutoRebuildAfterPush || !d.AutoRebuildAfterSync ||
		d.PushPatchesToOrigin || d.RebuildStrategy != "rebase" || d.RebuildFailMode != "fail_fast" || d.RebuildPushIntegrationBranch ||
		d.SquashMergeDetection != "both" || d.ForgeAPIURL != "" || d.UpstreamForgeAPIURL != "" {
		t.Errorf("DefaultSettings() = %+v", d)
	}
	base := d
	base.PatchBranchSource = "origin"
	base.RebuildStrategy = "merge"
	got, err := applySettings(base, json.RawMessage(`{"push_patches_to_origin": true, "rebuild_strategy": null, "forge_api_url": " https://api.example.test "}`))
	if err != nil || got.PatchBranchSource != "origin" || !got.PushPatchesToOrigin || got.RebuildStrategy != "rebase" || got.ForgeAPIURL != " https://api.example.test " {
		t.Errorf("applySettings = %+v, %v", got, err)
	}
	if reset, err := applySettings(base, json.RawMessage(`null`)); err != nil || reset != d {
		t.Errorf("null = %+v, %v", reset, err)
	}
	for body, want := range map[string]string{
		`[]`:                                      "settings must be an object",
		`{"colour": "blue"}`:                      `unknown setting "colour"`,
		`{"push_patches_to_origin": "yes"}`:       "setting push_patches_to_origin must be true or false",
		`{"patch_branch_source": "github"}`:       `setting patch_branch_source must be one of "hub", "origin"`,
		`{"forge_api_url": "http://insecure"}`:    "setting forge_api_url must be an absolute https:// URL",
		`{"forge_api_url": "https://u@h/p"}`:      "without userinfo",
		`{"upstream_forge_api_url": "https://"}`:  "with a host",
		`{"forge_api_url": "https://h/p?q=1"}`:    "query",
		`{"forge_api_url": 7}`:                    "setting forge_api_url must be a string",
	} {
		if _, err := applySettings(d, json.RawMessage(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("applySettings(%s) error = %v; want %q", body, err, want)
		}
	}
	if back := decodeSettings(encodeSettings(got)); back != got || decodeSettings("{}") != d || decodeSettings("") != d || decodeSettings("garbage") != d {
		t.Error("the stored document does not round-trip or an empty one is not the defaults")
	}
	if settingValue(d, "auto_rebuild_after_push") != "true" || settingValue(d, "push_patches_to_origin") != "false" || settingValue(d, "rebuild_strategy") != "rebase" || settingValue(d, "nope") != "" {
		t.Error("settingValue")
	}
}

// TestSettings_Reader: the forge client and the module read the settings by
// their old variable names; what is not a set setting of a workspace is not
// found, and a database failure is returned as is.
func TestSettings_Reader(t *testing.T) {
	v := newEnv(t)
	s := DefaultSettings()
	s.PatchBranchSource, s.PushPatchesToOrigin, s.ForgeAPIURL = "origin", true, "https://api.example.test"
	v.seed(t, &Workspace{Slug: "ws", OwnerID: owner, Settings: s})
	read := SettingsReader(v.db)
	for key, want := range map[string]string{
		"PATCH_BRANCH_SOURCE": "origin", "push_patches_to_origin": "true", "AUTO_REBUILD_AFTER_PUSH": "true",
		"REBUILD_STRATEGY": "rebase", "FORGE_API_URL": "https://api.example.test",
	} {
		if got, err := read("workspace", "ws", key); err != nil || got != want {
			t.Errorf("read(%s) = %q, %v; want %q", key, got, err, want)
		}
	}
	var notFound *secrets.NotFoundError
	for _, c := range []struct{ owner, slug, key string }{
		{"workspace", "ws", "UPSTREAM_FORGE_API_URL"}, {"workspace", "ws", "CHECK_COMMAND"},
		{"workspace", "nope", "PATCH_BRANCH_SOURCE"}, {"user", owner, "PATCH_BRANCH_SOURCE"},
	} {
		if _, err := read(c.owner, c.slug, c.key); !errors.As(err, &notFound) {
			t.Errorf("read(%s, %s, %s) error = %v; want *secrets.NotFoundError", c.owner, c.slug, c.key, err)
		}
	}
	testutil.Exec(t, v.db, `DROP TABLE workspaces`)
	if _, err := read("workspace", "ws", "PATCH_BRANCH_SOURCE"); err == nil || errors.As(err, &notFound) {
		t.Errorf("without the table: %v; want the database error", err)
	}
}

// TestSettings_MigrateVariables: the behaviour switches that were workspace
// variables move into the settings document once, values a setting cannot
// take and unreadable ones are dropped with a warning, other variables stay.
func TestSettings_MigrateVariables(t *testing.T) {
	v := newEnv(t)
	v.seed(t, &Workspace{Slug: "old", OwnerID: owner})
	v.seed(t, &Workspace{Slug: "plain", OwnerID: owner})
	store := secrets.NewStore(v.db)
	if _, err := store.CreateVariables("workspace", "old", []secrets.EntryInput{
		{Key: "PATCH_BRANCH_SOURCE", Value: "origin"}, {Key: "auto_rebuild_after_sync", Value: " False "},
		{Key: "REBUILD_STRATEGY", Value: "squash"}, {Key: "MY_OWN_VARIABLE", Value: "kept"},
	}); err != nil {
		t.Fatal(err)
	}
	// A variable of a workspace that is gone is deleted without a change.
	if _, err := store.CreateVariables("workspace", "gone", []secrets.EntryInput{{Key: "REBUILD_FAIL_MODE", Value: "continue"}}); err != nil {
		t.Fatal(err)
	}
	testutil.Exec(t, v.db, `UPDATE variables SET value = '%%%' WHERE owner_id = 'old' AND key = 'REBUILD_STRATEGY'`)
	n, err := MigrateVariableSettings(v.db)
	if err != nil || n != 1 {
		t.Fatalf("MigrateVariableSettings = %d, %v; want 1", n, err)
	}
	ws, _ := getWorkspaceBySlug(v.db, "old")
	if ws.Settings.PatchBranchSource != "origin" || ws.Settings.AutoRebuildAfterSync || ws.Settings.RebuildStrategy != "rebase" {
		t.Errorf("migrated settings = %+v", ws.Settings)
	}
	if vars, err := store.ListVariables("workspace", "old"); err != nil || len(vars) != 1 || vars[0].Key != "MY_OWN_VARIABLE" {
		t.Errorf("remaining variables = %v, %v; want only MY_OWN_VARIABLE", vars, err)
	}
	if testutil.Count(t, v.db, `SELECT COUNT(*) FROM variables WHERE owner_id = 'gone'`) != 0 {
		t.Error("the variable of the missing workspace stayed")
	}
	if n, err := MigrateVariableSettings(v.db); err != nil || n != 0 {
		t.Errorf("second run = %d, %v; want 0", n, err)
	}
	testutil.Exec(t, v.db, `DROP TABLE variables`)
	if _, err := MigrateVariableSettings(v.db); err == nil {
		t.Error("no error without the variables table")
	}
}
