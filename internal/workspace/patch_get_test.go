package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	_ "modernc.org/sqlite"
)

// TS-23-1: GET single patch returns the patch object plus the 40-character
// replaced_sha when a backup exists.
func TestGetPatch_ReplacedSHA_Present_TS231(t *testing.T) {
	const slug = "get-patch-sha"
	const patchID = "patch-uuid-1"
	const stubSHA = "aabbccddee00112233445566778899aabbccddee"

	env := newPatchTestEnv(t, slug, "main")
	seedPatchRaw(t, env.db, patchID, slug, "feature/x", 1)

	// Register a stub hook that returns a backup SHA.
	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })
	RegisterRecoveryHook(&stubRecoveryHook{
		readSHA:   stubSHA,
		readFound: true,
	})

	callers := map[string]*apikit.AuthInfo{
		"owner":     userAuth("user-1"),
		"admin":     adminAuth(),
		"pat_read":  patAuth("user-1", "patches:read"),
		"pat_write": patAuth("user-1", "patches:write"),
	}

	hexRe := regexp.MustCompile(`^[0-9a-f]{40}$`)

	for name, auth := range callers {
		t.Run(name, func(t *testing.T) {
			path := fmt.Sprintf("/api/v1/workspaces/%s/patches/%s", slug, patchID)
			rec := env.doRequest(t, http.MethodGet, path, "", auth)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
			}

			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}

			// Check replaced_sha.
			sha, ok := resp["replaced_sha"]
			if !ok {
				t.Fatal("response missing replaced_sha")
			}
			shaStr, _ := sha.(string)
			if shaStr != stubSHA {
				t.Errorf("replaced_sha = %q; want %q", shaStr, stubSHA)
			}
			if !hexRe.MatchString(shaStr) {
				t.Errorf("replaced_sha %q is not 40 hex chars", shaStr)
			}

			// Check existing patchResponse fields.
			if resp["id"] != patchID {
				t.Errorf("id = %v; want %q", resp["id"], patchID)
			}
			if resp["branch_name"] != "feature/x" {
				t.Errorf("branch_name = %v; want feature/x", resp["branch_name"])
			}
			if resp["status"] != "active" {
				t.Errorf("status = %v; want active", resp["status"])
			}
			if _, ok := resp["workspace_slug"]; !ok {
				t.Error("response missing workspace_slug")
			}
		})
	}
}

// TS-23-2: GET omits replaced_sha when there is no backup, no trunk on disk
// or no hook registered.
func TestGetPatch_ReplacedSHA_Omitted_TS232(t *testing.T) {
	const slug = "get-patch-no-sha"
	const patchID = "patch-uuid-2"

	env := newPatchTestEnv(t, slug, "main")
	seedPatchRaw(t, env.db, patchID, slug, "feature/y", 1)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })

	auth := userAuth("user-1")
	path := fmt.Sprintf("/api/v1/workspaces/%s/patches/%s", slug, patchID)

	cases := []struct {
		name string
		hook RecoveryHook
	}{
		{
			name: "no_ref_hook",
			hook: &stubRecoveryHook{readFound: false},
		},
		{
			name: "no_trunk_hook",
			hook: &stubRecoveryHook{readFound: false},
		},
		{
			name: "nil_hook",
			hook: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recoveryHook = tc.hook

			rec := env.doRequest(t, http.MethodGet, path, "", auth)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
			}

			// Check raw JSON for absence of replaced_sha.
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if _, ok := raw["replaced_sha"]; ok {
				t.Error("replaced_sha should not be present")
			}
		})
	}
}

// TS-23-3: A hook error while reading the backup ref is logged at warn level
// and does not fail GET.
func TestGetPatch_HookError_LoggedWarn_TS233(t *testing.T) {
	const slug = "get-patch-hook-err"
	const patchID = "patch-uuid-3"

	env := newPatchTestEnv(t, slug, "main")
	seedPatchRaw(t, env.db, patchID, slug, "feature/z", 1)

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })
	RegisterRecoveryHook(&stubRecoveryHook{
		readErr: errors.New("disk on fire"),
	})

	// Capture log output.
	var logBuf logBuffer
	handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	auth := userAuth("user-1")
	path := fmt.Sprintf("/api/v1/workspaces/%s/patches/%s", slug, patchID)
	rec := env.doRequest(t, http.MethodGet, path, "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}

	// No replaced_sha.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["replaced_sha"]; ok {
		t.Error("replaced_sha should not be present on hook error")
	}

	// Check for warn-level log.
	if logBuf.countLevel("WARN") < 1 {
		t.Error("expected at least one WARN log entry")
	}
}

// TS-23-4: GET returns a soft-deleted patch with status deleted.
func TestGetPatch_SoftDeleted_TS234(t *testing.T) {
	const slug = "get-patch-deleted"
	const patchID = "patch-uuid-4"

	env := newPatchTestEnv(t, slug, "main")

	// Insert a soft-deleted patch.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := env.db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, deleted_at, added_at, updated_at)
		 VALUES (?, ?, ?, ?, 'deleted', ?, ?, ?)`,
		patchID, slug, "feature/del", -1, now, now, now,
	)
	if err != nil {
		t.Fatalf("seed deleted patch: %v", err)
	}

	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })
	recoveryHook = nil

	auth := userAuth("user-1")
	path := fmt.Sprintf("/api/v1/workspaces/%s/patches/%s", slug, patchID)
	rec := env.doRequest(t, http.MethodGet, path, "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "deleted" {
		t.Errorf("status = %v; want deleted", resp["status"])
	}
}

// TS-23-5: GET with an unknown patch id answers 404 patch not found.
func TestGetPatch_UnknownID_404_TS235(t *testing.T) {
	const slug = "get-patch-unknown"

	env := newPatchTestEnv(t, slug, "main")

	auth := userAuth("user-1")
	path := fmt.Sprintf("/api/v1/workspaces/%s/patches/%s", slug, "nonexistent-id")
	rec := env.doRequest(t, http.MethodGet, path, "", auth)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404; body: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message != "patch not found" {
		t.Errorf("message = %q; want %q", resp.Error.Message, "patch not found")
	}
}

// TS-23-6: GET by a caller who is neither owner nor admin answers 404
// workspace not found.
func TestGetPatch_NonOwner_404_TS236(t *testing.T) {
	const slug = "get-patch-nonowner"
	const patchID = "patch-uuid-6"

	env := newPatchTestEnv(t, slug, "main")
	seedPatchRaw(t, env.db, patchID, slug, "feature/a", 1)

	// user-b is not the owner (owner is user-1).
	auth := userAuth("user-b")
	path := fmt.Sprintf("/api/v1/workspaces/%s/patches/%s", slug, patchID)
	rec := env.doRequest(t, http.MethodGet, path, "", auth)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404; body: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message != "workspace not found" {
		t.Errorf("message = %q; want %q", resp.Error.Message, "workspace not found")
	}
}

// TS-23-7: GET by a PAT with neither patches:read nor patches:write answers 403.
func TestGetPatch_NoPatchScope_403_TS237(t *testing.T) {
	const slug = "get-patch-noscope"
	const patchID = "patch-uuid-7"

	env := newPatchTestEnv(t, slug, "main")
	seedPatchRaw(t, env.db, patchID, slug, "feature/b", 1)

	auth := patAuth("user-1", "workspaces:read")
	path := fmt.Sprintf("/api/v1/workspaces/%s/patches/%s", slug, patchID)
	rec := env.doRequest(t, http.MethodGet, path, "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403; body: %s", rec.Code, rec.Body.String())
	}
}

// TS-23-8: No other patch endpoint ever returns replaced_sha.
func TestGetPatch_OtherEndpoints_NoReplacedSHA_TS238(t *testing.T) {
	const slug = "get-patch-no-sha-other"
	const patchID = "patch-uuid-8"

	env := newPatchTestEnv(t, slug, "main")
	seedPatchRaw(t, env.db, patchID, slug, "feature/c", 1)

	// Register a hook that always returns a SHA.
	saved := recoveryHook
	t.Cleanup(func() { recoveryHook = saved })
	RegisterRecoveryHook(&stubRecoveryHook{
		readSHA:   "aabbccddee00112233445566778899aabbccddee",
		readFound: true,
	})

	auth := userAuth("user-1")

	// List patches.
	t.Run("list", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
		assertNoReplacedSHAInBody(t, rec.Body.Bytes())
	})

	// Update patch.
	t.Run("update", func(t *testing.T) {
		path := fmt.Sprintf("/api/v1/workspaces/%s/patches/%s", slug, patchID)
		rec := env.doRequest(t, http.MethodPatch, path, `{"description":"updated"}`, auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
		}
		assertNoReplacedSHAInBody(t, rec.Body.Bytes())
	})

	// Add patch (should not have replaced_sha).
	t.Run("add", func(t *testing.T) {
		// Disable branch check hook to avoid needing a real repo.
		savedBCH := branchCheckHook
		branchCheckHook = nil
		t.Cleanup(func() { branchCheckHook = savedBCH })

		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches",
			`{"branch_name":"feature/new-for-test"}`, auth)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d; want 201; body: %s", rec.Code, rec.Body.String())
		}
		assertNoReplacedSHAInBody(t, rec.Body.Bytes())
	})
}

// assertNoReplacedSHAInBody checks that no patch object in the JSON body
// contains a replaced_sha key.
func assertNoReplacedSHAInBody(t *testing.T, body []byte) {
	t.Helper()

	// Try as array first.
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(body, &arr); err == nil {
		for i, obj := range arr {
			if _, ok := obj["replaced_sha"]; ok {
				t.Errorf("patch[%d] contains replaced_sha", i)
			}
		}
		return
	}

	// Try as single object.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err == nil {
		if _, ok := obj["replaced_sha"]; ok {
			t.Error("response contains replaced_sha")
		}
	}
}

// logBuffer captures slog output for testing.
type logBuffer struct {
	data []byte
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *logBuffer) countLevel(level string) int {
	count := 0
	lines := splitLines(b.data)
	for _, line := range lines {
		if containsLevel(line, level) {
			count++
		}
	}
	return count
}

func splitLines(data []byte) []string {
	var lines []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, string(data[start:i]))
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, string(data[start:]))
	}
	return lines
}

func containsLevel(line, level string) bool {
	// slog text handler writes level=WARN
	return regexp.MustCompile(`level=` + level).MatchString(line)
}
