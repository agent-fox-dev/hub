package workspace

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// Settings are the per-workspace behaviour switches: how a carry-patch
// workspace syncs, rebuilds and pushes, and where its forge's API is. They
// are given with the workspace at creation or changed with PATCH, validated
// on every write and stored as one JSON document in workspaces.settings. A key
// a stored document lacks has its default; the API shows every key with its
// effective value.
type Settings struct {
	// PatchBranchSource says who writes patch branches: "hub" (the hub's
	// clone is the authority) or "origin" (the fork is fetched on sync and
	// every registered patch branch follows the fork's tip).
	PatchBranchSource string `json:"patch_branch_source"`
	// PatchDivergencePolicy applies when the hub's and the fork's copy of a
	// patch branch diverged in origin mode: "replace" the hub's copy (the
	// old tip is kept under refs/hub/replaced/<branch>) or "report" it.
	PatchDivergencePolicy string `json:"patch_divergence_policy"`
	// PushPatchesToOrigin forwards (origin mode) or mirrors (hub mode) a
	// push to a registered patch branch to the fork.
	PushPatchesToOrigin bool `json:"push_patches_to_origin"`
	// AutoRebuildAfterPush enqueues a rebuild when a registered patch branch
	// is pushed.
	AutoRebuildAfterPush bool `json:"auto_rebuild_after_push"`
	// AutoRebuildAfterSync enqueues a rebuild when a sync moves the upstream
	// base or a patch branch.
	AutoRebuildAfterSync bool `json:"auto_rebuild_after_sync"`
	// RebuildStrategy applies patches by "rebase" (cherry-pick) or "merge";
	// a rebuild request may override it.
	RebuildStrategy string `json:"rebuild_strategy"`
	// RebuildFailMode stops at the first conflict ("fail_fast") or skips the
	// conflicting patch and continues ("continue"); a rebuild request may
	// override it.
	RebuildFailMode string `json:"rebuild_fail_mode"`
	// RebuildPushIntegrationBranch pushes the rebuilt integration branch to
	// the fork after a successful rebuild.
	RebuildPushIntegrationBranch bool `json:"rebuild_push_integration_branch"`
	// SquashMergeDetection chooses how a sync detects patches merged upstream:
	// "ancestry_only", "content_based" or "both".
	SquashMergeDetection string `json:"squash_merge_detection"`
	// ForgeAPIURL is the API URL of the forge behind git_url; "" finds it
	// from the host.
	ForgeAPIURL string `json:"forge_api_url"`
	// UpstreamForgeAPIURL is the same for the upstream_url of a carry_patch
	// workspace; it does not fall back to ForgeAPIURL.
	UpstreamForgeAPIURL string `json:"upstream_forge_api_url"`
	// SyncEnabled lets POST /sync fetch the workspace's origin; false refuses
	// every sync of the workspace.
	SyncEnabled bool `json:"sync_enabled"`
}

// DefaultSettings are the settings of a workspace that set none.
func DefaultSettings() Settings {
	return Settings{
		PatchBranchSource:     "hub",
		PatchDivergencePolicy: "replace",
		AutoRebuildAfterPush:  true,
		AutoRebuildAfterSync:  true,
		RebuildStrategy:       "rebase",
		RebuildFailMode:       "fail_fast",
		SquashMergeDetection:  "both",
		SyncEnabled:           true,
	}
}

// settingChoices are the values each enumerated setting accepts.
var settingChoices = map[string][]string{
	"patch_branch_source":     {"hub", "origin"},
	"patch_divergence_policy": {"replace", "report"},
	"rebuild_strategy":        {"rebase", "merge"},
	"rebuild_fail_mode":       {"fail_fast", "continue"},
	"squash_merge_detection":  {"ancestry_only", "content_based", "both"},
}

// settingKinds says what JSON type each setting takes: "enum", "bool" or
// "url". It is also the list of the keys a request may name.
var settingKinds = map[string]string{
	"patch_branch_source":             "enum",
	"patch_divergence_policy":         "enum",
	"push_patches_to_origin":          "bool",
	"auto_rebuild_after_push":         "bool",
	"auto_rebuild_after_sync":         "bool",
	"rebuild_strategy":                "enum",
	"rebuild_fail_mode":               "enum",
	"rebuild_push_integration_branch": "bool",
	"squash_merge_detection":          "enum",
	"forge_api_url":                   "url",
	"upstream_forge_api_url":          "url",
	"sync_enabled":                    "bool",
}

// decodeSettings reads a stored settings document: the defaults, overlaid
// with the keys the document has. An unreadable document gives the defaults.
func decodeSettings(raw string) Settings {
	s := DefaultSettings()
	if raw == "" {
		return s
	}
	_ = json.Unmarshal([]byte(raw), &s)
	return s
}

// encodeSettings writes the settings document that is stored.
func encodeSettings(s Settings) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// applySettings overlays a request's settings object on base: a key given
// with a value takes it, a key given as null returns to its default, a key
// not given keeps base's value. It answers the error the request is refused
// with: an object of another shape, an unknown key, a value of the wrong type
// or an enumerated value outside its choices.
func applySettings(base Settings, raw json.RawMessage) (Settings, error) {
	if string(raw) == "null" {
		return DefaultSettings(), nil
	}
	var given map[string]json.RawMessage
	if err := json.Unmarshal(raw, &given); err != nil {
		return base, fmt.Errorf("settings must be an object")
	}
	defaults := DefaultSettings()
	defaultsJSON, _ := json.Marshal(defaults)
	var defaultsByKey map[string]json.RawMessage
	_ = json.Unmarshal(defaultsJSON, &defaultsByKey)

	merged := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(encodeSettings(base)), &merged)
	for key, value := range given {
		kind, known := settingKinds[key]
		if !known {
			return base, fmt.Errorf("unknown setting %q", key)
		}
		if string(value) == "null" {
			merged[key] = defaultsByKey[key]
			continue
		}
		switch kind {
		case "bool":
			var b bool
			if err := json.Unmarshal(value, &b); err != nil {
				return base, fmt.Errorf("setting %s must be true or false", key)
			}
		case "enum":
			var v string
			if err := json.Unmarshal(value, &v); err != nil || !slices.Contains(settingChoices[key], v) {
				return base, fmt.Errorf("setting %s must be one of %s", key, quoteAll(settingChoices[key]))
			}
		case "url":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return base, fmt.Errorf("setting %s must be a string", key)
			}
			if v != "" && !validSettingURL(v) {
				return base, fmt.Errorf("setting %s must be an absolute https:// URL with a host and without userinfo, query or fragment, or empty", key)
			}
		}
		merged[key] = value
	}
	mergedJSON, _ := json.Marshal(merged)
	out := DefaultSettings()
	if err := json.Unmarshal(mergedJSON, &out); err != nil {
		return base, fmt.Errorf("settings must be an object")
	}
	return out, nil
}

// validSettingURL accepts the forge API URLs the forge client accepts.
func validSettingURL(v string) bool {
	u, err := url.Parse(strings.TrimSpace(v))
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func quoteAll(list []string) string {
	q := make([]string, len(list))
	for i, x := range list {
		q[i] = fmt.Sprintf("%q", x)
	}
	return strings.Join(q, ", ")
}

// legacySettingKeys maps the names the settings had as workspace variables to
// the setting keys. The carry-patch module and the forge client still read
// settings by these names, through SettingsReader.
var legacySettingKeys = map[string]string{
	"PATCH_BRANCH_SOURCE":             "patch_branch_source",
	"PATCH_DIVERGENCE_POLICY":         "patch_divergence_policy",
	"PUSH_PATCHES_TO_ORIGIN":          "push_patches_to_origin",
	"AUTO_REBUILD_AFTER_PUSH":         "auto_rebuild_after_push",
	"AUTO_REBUILD_AFTER_SYNC":         "auto_rebuild_after_sync",
	"REBUILD_STRATEGY":                "rebuild_strategy",
	"REBUILD_FAIL_MODE":               "rebuild_fail_mode",
	"REBUILD_PUSH_INTEGRATION_BRANCH": "rebuild_push_integration_branch",
	"SQUASH_MERGE_DETECTION":          "squash_merge_detection",
	"FORGE_API_URL":                   "forge_api_url",
	"UPSTREAM_FORGE_API_URL":          "upstream_forge_api_url",
}

// settingValue is the string form of one setting, by key: "true" or "false"
// for a boolean, the value itself otherwise.
func settingValue(s Settings, key string) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(encodeSettings(s)), &m)
	switch v := m[key].(type) {
	case bool:
		if v {
			return "true"
		}
		return "false"
	case string:
		return v
	}
	return ""
}

// SettingsReader returns the function the forge client and the carry-patch
// module read a workspace's settings through, by the names the settings had
// as variables ("PATCH_BRANCH_SOURCE", ...). A name that is not a setting, a
// workspace that does not exist and an empty forge API URL answer
// *secrets.NotFoundError, which the readers take as "not set"; a database
// failure is returned as is.
func SettingsReader(db *sql.DB) func(ownerType, ownerID, key string) (string, error) {
	return func(ownerType, ownerID, key string) (string, error) {
		settingKey, ok := legacySettingKeys[strings.ToUpper(key)]
		if ownerType != "workspace" || !ok {
			return "", &secrets.NotFoundError{Key: key}
		}
		var raw string
		err := db.QueryRow(`SELECT settings FROM workspaces WHERE slug = ?`, ownerID).Scan(&raw)
		if err == sql.ErrNoRows {
			return "", &secrets.NotFoundError{Key: key}
		}
		if err != nil {
			return "", fmt.Errorf("read settings of workspace %q: %w", ownerID, err)
		}
		v := settingValue(decodeSettings(raw), settingKey)
		if v == "" {
			return "", &secrets.NotFoundError{Key: key}
		}
		return v, nil
	}
}

// MigrateVariableSettings moves the behaviour switches that used to be
// workspace variables into the workspaces' settings documents and deletes the
// variable rows, once: a workspace whose variables name one of the legacy
// keys gets the values it can take (a value a setting cannot take is dropped
// with a warning). It returns the number of workspaces changed; a second run
// finds no legacy variables.
func MigrateVariableSettings(db *sql.DB) (int, error) {
	keys := make([]any, 0, len(legacySettingKeys))
	marks := make([]string, 0, len(legacySettingKeys))
	for k := range legacySettingKeys {
		keys = append(keys, k)
		marks = append(marks, "?")
	}
	rows, err := db.Query(`SELECT owner_id, key, value FROM variables WHERE owner_type = 'workspace' AND UPPER(key) IN (`+strings.Join(marks, ",")+`) ORDER BY owner_id`, keys...)
	if err != nil {
		return 0, fmt.Errorf("read legacy workspace variables: %w", err)
	}
	type variable struct{ slug, key, value string }
	var found []variable
	for rows.Next() {
		var v variable
		var encoded string
		if err := rows.Scan(&v.slug, &v.key, &encoded); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan legacy workspace variables: %w", err)
		}
		if v.value, err = secrets.DecodeValue(encoded); err != nil {
			slog.Warn("settings migration: unreadable variable value dropped", "workspace", v.slug, "key", v.key)
			v.value = ""
		}
		found = append(found, v)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(found) == 0 {
		return 0, nil
	}
	bySlug := map[string]map[string]json.RawMessage{}
	var order []string
	for _, v := range found {
		if _, seen := bySlug[v.slug]; !seen {
			bySlug[v.slug] = map[string]json.RawMessage{}
			order = append(order, v.slug)
		}
		settingKey := legacySettingKeys[strings.ToUpper(v.key)]
		var value json.RawMessage
		switch settingKinds[settingKey] {
		case "bool":
			value = json.RawMessage(fmt.Sprintf("%v", strings.EqualFold(strings.TrimSpace(v.value), "true")))
		default:
			b, _ := json.Marshal(strings.TrimSpace(v.value))
			value = b
		}
		bySlug[v.slug][settingKey] = value
	}
	changed := 0
	for _, slug := range order {
		var raw string
		err := db.QueryRow(`SELECT settings FROM workspaces WHERE slug = ?`, slug).Scan(&raw)
		if err != nil && err != sql.ErrNoRows {
			return changed, fmt.Errorf("read settings of workspace %q: %w", slug, err)
		}
		settings := decodeSettings(raw)
		for key, value := range bySlug[slug] {
			one, _ := json.Marshal(map[string]json.RawMessage{key: value})
			next, applyErr := applySettings(settings, one)
			if applyErr != nil {
				slog.Warn("settings migration: variable value dropped", "workspace", slug, "setting", key, "error", applyErr)
				continue
			}
			settings = next
		}
		if err == nil {
			if _, err := db.Exec(`UPDATE workspaces SET settings = ? WHERE slug = ?`, encodeSettings(settings), slug); err != nil {
				return changed, fmt.Errorf("write settings of workspace %q: %w", slug, err)
			}
			changed++
		}
		if _, err := db.Exec(`DELETE FROM variables WHERE owner_type = 'workspace' AND owner_id = ? AND UPPER(key) IN (`+strings.Join(marks, ",")+`)`, append([]any{slug}, keys...)...); err != nil {
			return changed, fmt.Errorf("delete legacy variables of workspace %q: %w", slug, err)
		}
	}
	return changed, nil
}
