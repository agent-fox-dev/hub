package carrypatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const patchesPath = "/api/v1/workspaces/ws/patches"

// patchEnv is an env with an active carry_patch workspace "ws" owned by alice.
func patchEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	seedWS(t, e.db, ws{slug: "ws"})
	return e
}

func TestAddPatch_AppendsWithoutPositionAndShiftsWithOne(t *testing.T) {
	e := patchEnv(t)
	first := e.want(e.do(http.MethodPost, patchesPath, `{"branch_name":"feature/a","description":"d","upstream_pr_url":"https://github.com/o/r/pull/1"}`, alice), http.StatusCreated)
	if first["position"] != 1.0 || first["status"] != "active" || first["description"] != "d" || first["upstream_pr_url"] != "https://github.com/o/r/pull/1" || first["deleted_at"] != nil || first["id"] == "" {
		t.Errorf("first patch = %v", first)
	}
	e.want(e.do(http.MethodPost, patchesPath, `{"branch_name":"feature/b"}`, alice), http.StatusCreated)
	// An explicit position before the last patch shifts it down; one past the
	// end is clamped to an append.
	mid := e.want(e.do(http.MethodPost, patchesPath, `{"branch_name":"feature/mid","position":2}`, alice), http.StatusCreated)
	if mid["position"] != 2.0 {
		t.Errorf("explicit position = %v", mid["position"])
	}
	far := e.want(e.do(http.MethodPost, patchesPath, `{"branch_name":"feature/far","position":50}`, alice), http.StatusCreated)
	if far["position"] != 4.0 {
		t.Errorf("clamped position = %v", far["position"])
	}
	if got := positions(t, e.db, "ws"); !equalStrings(got, []string{"feature/a", "feature/mid", "feature/b", "feature/far"}) {
		t.Errorf("order = %v", got)
	}
}

// The single add refuses a bad request before it writes anything.
func TestAddPatch_RejectsInvalidRequests(t *testing.T) {
	e := patchEnv(t)
	seedWS(t, e.db, ws{slug: "std", mode: "standard"})
	seedWS(t, e.db, ws{slug: "archived", status: "archived"})
	seedPatch(t, e.db, "ws", "p1", "feature/exists", 1, "active")

	cases := []struct {
		name, slug, body, want string
		status                 int
	}{
		{"missing branch", "ws", `{}`, "branch_name is required", 400},
		{"integration branch", "ws", `{"branch_name":"integration"}`, "branch_name cannot be the integration branch", 400},
		{"position below one", "ws", `{"branch_name":"feature/x","position":0}`, "position must be >= 1", 400},
		{"duplicate", "ws", `{"branch_name":"feature/exists"}`, "branch already exists in patch list", 409},
		{"invalid json", "ws", `{"branch_name":`, "invalid request body", 400},
		{"standard workspace", "std", `{"branch_name":"feature/x"}`, "workspace is not in carry_patch mode", 400},
		{"archived workspace", "archived", `{"branch_name":"feature/x"}`, "workspace is archived", 400},
		{"unknown workspace", "none", `{"branch_name":"feature/x"}`, "workspace not found", 404},
	}
	for _, c := range cases {
		msg, _ := e.wantErr(e.do(http.MethodPost, "/api/v1/workspaces/"+c.slug+"/patches", c.body, alice), c.status)
		if !strings.HasPrefix(msg, c.want) {
			t.Errorf("%s: message = %q, want prefix %q", c.name, msg, c.want)
		}
	}
	if len(positions(t, e.db, "ws")) != 1 {
		t.Error("a refused request wrote a row")
	}
}

// Branch names follow git's ref rules; each rule names its reason.
func TestAddPatch_ValidatesBranchName(t *testing.T) {
	e := patchEnv(t)
	bad := map[string]string{
		"has space": "spaces", "a~b": "'~'", "a^b": "'^'", "a:b": "':'", "a?b": "'?'", "a*b": "'*'",
		"a[b": "'['", "a\\b": "'\\'", "a\x01b": "control", "a..b": "'..'", "a.lock": "'.lock'",
		"a.": "end with '.'", ".a": "leading dot", "x/.a": "leading dot", "-x": "start with '-'",
		"a//b": "'//'", "a@{b": "'@{'", "@": "'@'",
	}
	for name, reason := range bad {
		body, _ := json.Marshal(map[string]string{"branch_name": name})
		msg, _ := e.wantErr(e.do(http.MethodPost, patchesPath, string(body), alice), http.StatusBadRequest)
		if !strings.HasPrefix(msg, "invalid branch_name:") || !strings.Contains(msg, reason) {
			t.Errorf("%q: message = %q, want reason %q", name, msg, reason)
		}
	}
	for _, name := range []string{"feature/a-b_c.d", "release/v1.2", "UPPER", "a/b/c"} {
		e.want(e.do(http.MethodPost, patchesPath, fmt.Sprintf(`{"branch_name":%q}`, name), alice), http.StatusCreated)
	}
	if err := validateBranchName(""); err == nil {
		t.Error("empty name accepted")
	}
}

// if_not_exists answers the existing record with 200 instead of 409.
func TestAddPatch_IfNotExistsReturnsExistingRecord(t *testing.T) {
	e := patchEnv(t)
	seedPatch(t, e.db, "ws", "p1", "feature/a", 1, "active")
	got := e.want(e.do(http.MethodPost, patchesPath, `{"branch_name":"feature/a","if_not_exists":true}`, alice), http.StatusOK)
	if got["id"] != "p1" {
		t.Errorf("existing record = %v", got)
	}
	created := e.want(e.do(http.MethodPost, patchesPath, `{"branch_name":"feature/b","if_not_exists":true}`, alice), http.StatusCreated)
	if created["position"] != 2.0 {
		t.Errorf("new record = %v", created)
	}
	if len(e.audit.ofType("hub.patch.create")) != 1 {
		t.Error("an if_not_exists hit must not emit a create event")
	}
}

// The branch resolver's answer decides the add: its method goes into the
// audit event, skip_branch_check bypasses it, and each error kind has its
// HTTP answer.
func TestAddPatch_BranchResolution(t *testing.T) {
	e := patchEnv(t)
	var resolved []string
	var answer error
	method := ResolutionOriginTracking
	e.resolve = func(_ context.Context, _, branch string) (string, error) {
		resolved = append(resolved, branch)
		return method, answer
	}

	e.want(e.do(http.MethodPost, patchesPath, `{"branch_name":"feature/a"}`, alice), http.StatusCreated)
	e.want(e.do(http.MethodPost, patchesPath, `{"branch_name":"feature/b","skip_branch_check":true}`, alice), http.StatusCreated)
	if !equalStrings(resolved, []string{"feature/a"}) {
		t.Errorf("resolved = %v, want only feature/a", resolved)
	}
	events := e.audit.ofType("hub.patch.create")
	if len(events) != 2 || events[0].Metadata["branch_resolution"] != ResolutionOriginTracking || events[1].Metadata["branch_resolution"] != ResolutionSkipped {
		t.Errorf("branch_resolution in audit = %v", events)
	}

	cases := []struct {
		err       error
		status    int
		msg, kind string
	}{
		{newBranchResolveError(BranchResolveKindNotFound, nil), 400, "branch does not exist in repository or on origin", ""},
		{newBranchResolveError(BranchResolveKindOriginFetchFailed, nil), 502, "origin fetch failed", "origin_fetch_failed"},
		{newBranchResolveError(BranchResolveKindOriginCredentials, nil), 502, "failed to resolve origin credentials", ""},
		{newBranchResolveError(BranchResolveKindWorkspaceBusy, nil), 409, "another operation is running on this workspace; retry later", "workspace_busy"},
		{errors.New("disk on fire"), 500, "internal server error", ""},
	}
	for _, c := range cases {
		answer = c.err
		msg, kind := e.wantErr(e.do(http.MethodPost, patchesPath, `{"branch_name":"feature/c"}`, alice), c.status)
		if msg != c.msg || kind != c.kind {
			t.Errorf("%v: %d %q/%q", c.err, c.status, msg, kind)
		}
	}
	if _, ok := patchStatuses(t, e.db, "ws")["feature/c"]; ok {
		t.Error("a refused resolution wrote a row")
	}
	// The resolver is consulted before the duplicate check.
	answer = newBranchResolveError(BranchResolveKindNotFound, nil)
	e.wantErr(e.do(http.MethodPost, patchesPath, `{"branch_name":"feature/a"}`, alice), http.StatusBadRequest)
}

// A batch validates every element, resolves them all in order before any
// insert, and inserts atomically; it emits no per-patch audit events.
func TestAddPatch_Batch(t *testing.T) {
	e := patchEnv(t)
	seedPatch(t, e.db, "ws", "p1", "feature/exists", 1, "active")
	var resolved []string
	failOn := ""
	e.resolve = func(_ context.Context, _ string, branch string) (string, error) {
		resolved = append(resolved, branch)
		if branch == failOn {
			return "", newBranchResolveError(BranchResolveKindNotFound, nil)
		}
		return ResolutionLocal, nil
	}

	list := e.wantList(e.do(http.MethodPost, patchesPath, `[{"branch_name":"feature/a"},{"branch_name":"feature/first","position":1},{"branch_name":"feature/b","skip_branch_check":true}]`, alice), http.StatusCreated)
	if !equalStrings(branches(list), []string{"feature/a", "feature/first", "feature/b"}) || list[1]["position"] != 1.0 {
		t.Errorf("batch response = %v", list)
	}
	if got := positions(t, e.db, "ws"); !equalStrings(got, []string{"feature/first", "feature/exists", "feature/a", "feature/b"}) {
		t.Errorf("order = %v", got)
	}
	if !equalStrings(resolved, []string{"feature/a", "feature/first"}) {
		t.Errorf("resolved = %v", resolved)
	}
	if len(e.audit.ofType("hub.patch.create")) != 0 {
		t.Error("batch emitted per-patch create events")
	}

	cases := []struct {
		name, body, want string
		status           int
	}{
		{"empty", `[]`, "batch request must contain at least one patch", 400},
		{"missing name", `[{"branch_name":"feature/x"},{}]`, "patch[1]: branch_name is required", 400},
		{"invalid name", `[{"branch_name":"bad name"}]`, "patch[0]: invalid branch_name", 400},
		{"integration", `[{"branch_name":"integration"}]`, "patch[0]: branch_name cannot be the integration branch", 400},
		{"position", `[{"branch_name":"feature/x","position":0}]`, "patch[0]: position must be >= 1", 400},
		{"duplicate within", `[{"branch_name":"feature/x"},{"branch_name":"feature/x"}]`, `patch[1]: duplicate branch_name "feature/x" in batch`, 409},
		{"exists", `[{"branch_name":"feature/y"},{"branch_name":"feature/exists"}]`, `patch[1]: branch "feature/exists" already exists in patch list`, 409},
		{"malformed", `[{"branch_name":`, "invalid request body", 400},
	}
	for _, c := range cases {
		msg, _ := e.wantErr(e.do(http.MethodPost, patchesPath, c.body, alice), c.status)
		if !strings.HasPrefix(msg, c.want) {
			t.Errorf("%s: %q", c.name, msg)
		}
	}
	// A resolution failure names the element and leaves earlier ones uninserted.
	resolved, failOn = nil, "feature/n2"
	msg, _ := e.wantErr(e.do(http.MethodPost, patchesPath, `[{"branch_name":"feature/n1"},{"branch_name":"feature/n2"}]`, alice), http.StatusBadRequest)
	if msg != "patch[1]: branch does not exist in repository or on origin" {
		t.Errorf("resolution failure = %q", msg)
	}
	if _, ok := patchStatuses(t, e.db, "ws")["feature/n1"]; ok {
		t.Error("earlier element inserted although a later one failed")
	}
	if len(positions(t, e.db, "ws")) != 4 {
		t.Error("a refused batch changed the rows")
	}
}

func TestListPatches_OrderedVisibleRowsWithConflictFiles(t *testing.T) {
	e := patchEnv(t)
	seedWS(t, e.db, ws{slug: "std", mode: "standard"})
	seedPatch(t, e.db, "ws", "p2", "feature/b", 2, "conflict")
	seedPatch(t, e.db, "ws", "p1", "feature/a", 1, "active")
	seedPatch(t, e.db, "ws", "p3", "feature/gone", -3, "deleted")
	mustExec(t, e.db, `UPDATE patches SET conflict_files = '["x.go"]' WHERE id = 'p2'`)

	list := e.wantList(e.do(http.MethodGet, patchesPath, "", alice), http.StatusOK)
	if !equalStrings(branches(list), []string{"feature/a", "feature/b"}) {
		t.Errorf("list = %v", branches(list))
	}
	if files, _ := list[1]["conflict_files"].([]any); len(files) != 1 || files[0] != "x.go" {
		t.Errorf("conflict_files = %v", list[1]["conflict_files"])
	}
	if _, ok := list[0]["conflict_files"]; ok {
		t.Error("conflict_files present on a clean patch")
	}
	if l := e.wantList(e.do(http.MethodGet, "/api/v1/workspaces/std/patches", "", alice), http.StatusOK); len(l) != 0 {
		t.Errorf("standard workspace list = %v, want []", l)
	}
	mustExec(t, e.db, `DELETE FROM patches`)
	if body := e.do(http.MethodGet, patchesPath, "", alice).Body.String(); strings.TrimSpace(body) != "[]" {
		t.Errorf("empty list body = %q", body)
	}
}

// GET returns soft-deleted patches too and adds replaced_sha when the
// recovery hook knows a backup ref; a hook failure is logged and ignored.
func TestGetPatch_ReplacedSHAFromRecoveryHook(t *testing.T) {
	e := patchEnv(t)
	seedPatch(t, e.db, "ws", "p1", "feature/a", 1, "active")
	seedPatch(t, e.db, "ws", "p2", "feature/gone", -2, "deleted")
	mustExec(t, e.db, `UPDATE patches SET deleted_at = '2024-01-01T00:00:00Z' WHERE id = 'p2'`)
	get := func(id string, status int) map[string]any {
		return e.want(e.do(http.MethodGet, patchesPath+"/"+id, "", alice), status)
	}

	if got := get("p1", http.StatusOK); got["branch_name"] != "feature/a" {
		t.Errorf("no hook = %v", got)
	}
	if _, ok := get("p1", http.StatusOK)["replaced_sha"]; ok {
		t.Error("replaced_sha present without a recovery hook")
	}
	stub := &stubRecovery{sha: "abc", found: true}
	e.recovery = stub
	if got := get("p1", http.StatusOK); got["replaced_sha"] != "abc" {
		t.Errorf("replaced_sha = %v", got["replaced_sha"])
	}
	stub.found = false
	if _, ok := get("p1", http.StatusOK)["replaced_sha"]; ok {
		t.Error("replaced_sha present without a backup ref")
	}
	stub.readErr = errors.New("git broken")
	sink := captureDefaultLog(t)
	if _, ok := get("p1", http.StatusOK)["replaced_sha"]; ok || !strings.Contains(sink.String(), "failed to read replaced SHA") {
		t.Error("hook failure must be logged and omit replaced_sha")
	}
	if got := get("p2", http.StatusOK); got["status"] != "deleted" || got["deleted_at"] == nil {
		t.Errorf("soft-deleted patch = %v", got)
	}
	get("nope", http.StatusNotFound)
	if list := e.wantList(e.do(http.MethodGet, patchesPath, "", alice), http.StatusOK); len(list) != 1 {
		t.Error("replaced_sha hook changed the list route")
	}
}

func TestUpdatePatch_FieldsPositionAndValidation(t *testing.T) {
	e := patchEnv(t)
	for i, b := range []string{"a", "b", "c"} {
		seedPatch(t, e.db, "ws", "p"+b, "feature/"+b, i+1, "active")
	}
	seedPatch(t, e.db, "ws", "pd", "feature/d", -4, "deleted")
	patch := func(id, body string, status int) map[string]any {
		return e.want(e.do(http.MethodPatch, patchesPath+"/"+id, body, alice), status)
	}

	got := patch("pa", `{"status":"disabled","description":"why","upstream_pr_url":"https://github.com/o/r/pull/2","branch_name":"renamed"}`, http.StatusOK)
	if got["status"] != "disabled" || got["description"] != "why" || got["upstream_pr_url"] != "https://github.com/o/r/pull/2" || got["branch_name"] != "feature/a" {
		t.Errorf("updated = %v", got)
	}
	if got := patch("pc", `{"position":1}`, http.StatusOK); got["position"] != 1.0 {
		t.Errorf("moved = %v", got)
	}
	if got := positions(t, e.db, "ws"); !equalStrings(got, []string{"feature/c", "feature/a", "feature/b"}) {
		t.Errorf("order = %v", got)
	}
	for _, s := range []string{"active", "merged_upstream", "conflict", "disabled"} {
		patch("pb", fmt.Sprintf(`{"status":%q}`, s), http.StatusOK)
	}
	cases := []struct {
		name, id, body, want string
		status               int
	}{
		{"invalid status", "pb", `{"status":"bogus"}`, "invalid status value", 400},
		{"deleted status", "pb", `{"status":"deleted"}`, "invalid status value", 400},
		{"soft-deleted patch", "pd", `{"status":"active"}`, "patch is soft-deleted; restore it first", 409},
		{"position out of range", "pb", `{"position":4}`, "position out of range", 400},
		{"position zero", "pb", `{"position":0}`, "position out of range", 400},
		{"unknown", "nope", `{"status":"active"}`, "patch not found", 404},
		{"malformed", "pb", `{"status":`, "invalid request body", 400},
	}
	for _, c := range cases {
		msg, _ := e.wantErr(e.do(http.MethodPatch, patchesPath+"/"+c.id, c.body, alice), c.status)
		if !strings.HasPrefix(msg, c.want) {
			t.Errorf("%s: %q", c.name, msg)
		}
	}
	if got := patch("pb", `{}`, http.StatusOK); got["status"] != "disabled" {
		t.Errorf("empty object changed the patch: %v", got)
	}
}

// DELETE removes the row, compacts positions, records an audit event and
// removes the backup ref through the recovery hook; a failing hook or an
// absent one changes nothing about the answer.
func TestRemovePatch_CompactsAuditsAndRemovesBackup(t *testing.T) {
	e := patchEnv(t)
	for i, b := range []string{"a", "b", "c"} {
		seedPatch(t, e.db, "ws", "p"+b, "feature/"+b, i+1, "active")
	}
	stub := &stubRecovery{removeErr: errors.New("ref locked")}
	e.recovery = stub
	sink := captureDefaultLog(t)

	e.want(e.do(http.MethodDelete, patchesPath+"/pb", "", alice), http.StatusNoContent)
	if got := positions(t, e.db, "ws"); !equalStrings(got, []string{"feature/a", "feature/c"}) {
		t.Errorf("order = %v", got)
	}
	if !equalStrings(stub.removed, []string{"feature/b"}) || !strings.Contains(sink.String(), "failed to remove backup ref") {
		t.Errorf("backup removal: removed=%v logs=%s", stub.removed, sink.String())
	}
	events := e.audit.ofType("hub.patch.delete")
	if len(events) != 1 || events[0].ResourceID != "feature/b" || events[0].Metadata["branch_name"] != "feature/b" || events[0].ActorID != "alice" {
		t.Errorf("delete events = %v", events)
	}
	e.recovery = nil
	e.want(e.do(http.MethodDelete, patchesPath+"/pa", "", alice), http.StatusNoContent)
	msg, _ := e.wantErr(e.do(http.MethodDelete, patchesPath+"/pa", "", alice), http.StatusNotFound)
	if msg != "patch not found" || len(e.audit.ofType("hub.patch.delete")) != 2 {
		t.Errorf("second delete = %q, events = %d", msg, len(e.audit.ofType("hub.patch.delete")))
	}
}

func TestRestorePatch_AppendsSoftDeletedPatch(t *testing.T) {
	e := patchEnv(t)
	seedWS(t, e.db, ws{slug: "std", mode: "standard"})
	seedWS(t, e.db, ws{slug: "archived", status: "archived"})
	seedPatch(t, e.db, "ws", "pa", "feature/a", 1, "active")
	seedPatch(t, e.db, "ws", "pd", "feature/d", -2, "deleted")
	mustExec(t, e.db, `UPDATE patches SET deleted_at = '2024-01-01T00:00:00Z' WHERE id = 'pd'`)

	got := e.want(e.do(http.MethodPost, patchesPath+"/pd/restore", "", alice), http.StatusOK)
	if got["status"] != "active" || got["position"] != 2.0 || got["deleted_at"] != nil {
		t.Errorf("restored = %v", got)
	}
	cases := []struct {
		name, slug, id, want string
		status               int
	}{
		{"not deleted", "ws", "pa", "patch is not in deleted status", 400},
		{"unknown", "ws", "nope", "patch not found", 404},
		{"standard", "std", "pa", "workspace is not in carry_patch mode", 400},
		{"archived", "archived", "pa", "workspace is archived", 400},
	}
	for _, c := range cases {
		msg, _ := e.wantErr(e.do(http.MethodPost, "/api/v1/workspaces/"+c.slug+"/patches/"+c.id+"/restore", "", alice), c.status)
		if msg != c.want {
			t.Errorf("%s: %q", c.name, msg)
		}
	}
	// Restoring into an empty visible set gives position 1.
	mustExec(t, e.db, `UPDATE patches SET status = 'deleted', position = -rowid WHERE id IN ('pa','pd')`)
	if got := e.want(e.do(http.MethodPost, patchesPath+"/pa/restore", "", alice), http.StatusOK); got["position"] != 1.0 {
		t.Errorf("restore into empty set = %v", got["position"])
	}
}

func TestReorderPatches_ValidatesTheList(t *testing.T) {
	e := patchEnv(t)
	for i, b := range []string{"a", "b"} {
		seedPatch(t, e.db, "ws", "p"+b, "feature/"+b, i+1, "active")
	}
	list := e.wantList(e.do(http.MethodPost, patchesPath+"/reorder", `{"patch_ids":["pb","pa"]}`, alice), http.StatusOK)
	if !equalStrings(branches(list), []string{"feature/b", "feature/a"}) {
		t.Errorf("reordered = %v", branches(list))
	}
	for name, body := range map[string]string{
		"missing": `{"patch_ids":["pa"]}`, "duplicate": `{"patch_ids":["pa","pa"]}`, "foreign": `{"patch_ids":["pa","zz"]}`,
		"empty": `{"patch_ids":[]}`, "malformed": `{"patch_ids":`,
	} {
		if rec := e.do(http.MethodPost, patchesPath+"/reorder", body, alice); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", name, rec.Code)
		}
	}
	if got := positions(t, e.db, "ws"); !equalStrings(got, []string{"feature/b", "feature/a"}) {
		t.Errorf("a refused reorder changed the order: %v", got)
	}
}

// Every patch route refuses a PAT without its scope and an anonymous caller.
func TestPatchRoutes_ScopeMatrix(t *testing.T) {
	e := patchEnv(t)
	seedPatch(t, e.db, "ws", "p1", "feature/a", 1, "active")
	routes := []struct {
		method, path, scope string
	}{
		{http.MethodGet, patchesPath, "patches:read"},
		{http.MethodGet, patchesPath + "/p1", "patches:read"},
		{http.MethodPost, patchesPath, "patches:write"},
		{http.MethodPatch, patchesPath + "/p1", "patches:write"},
		{http.MethodDelete, patchesPath + "/p1", "patches:write"},
		{http.MethodPost, patchesPath + "/reorder", "patches:write"},
		{http.MethodPost, patchesPath + "/p1/restore", "patches:write"},
		{http.MethodPost, patchesPath + "/p1/reset-to-origin", "patches:write"},
	}
	for _, r := range routes {
		if rec := e.do(r.method, r.path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous = %d", r.method, r.path, rec.Code)
		}
		msg, _ := e.wantErr(e.do(r.method, r.path, "", pat("alice", "workspaces:write")), http.StatusForbidden)
		if msg != "PAT requires "+r.scope+" scope" {
			t.Errorf("%s %s without scope = %q", r.method, r.path, msg)
		}
		if rec := e.do(r.method, r.path, "", pat("alice", r.scope)); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s with scope = %d", r.method, r.path, rec.Code)
		}
	}
	// patches:write also grants the read routes.
	if rec := e.do(http.MethodGet, patchesPath, "", pat("alice", "patches:write")); rec.Code != http.StatusOK {
		t.Errorf("write scope on read route = %d", rec.Code)
	}
}

// The reset-to-origin route checks its preconditions, hands the patch to the
// recovery hook and renders the result with replaced_sha and the rebuild
// fields; it audits the reset (and the replacement) only on success.
func TestResetPatchToOrigin_Route(t *testing.T) {
	e := patchEnv(t)
	seedWS(t, e.db, ws{slug: "std", mode: "standard"})
	seedWS(t, e.db, ws{slug: "archived", status: "archived"})
	seedWS(t, e.db, ws{slug: "cloning", status: "cloning"})
	seedPatch(t, e.db, "ws", "p1", "feature/a", 1, "active")
	seedPatch(t, e.db, "ws", "pi", "integration", 2, "active")
	seedPatch(t, e.db, "ws", "pm", "feature/m", 3, "merged_upstream")
	seedPatch(t, e.db, "ws", "pd", "feature/d", 4, "disabled")
	reset := func(slug, id string) (int, string, string) {
		rec := e.do(http.MethodPost, "/api/v1/workspaces/"+slug+"/patches/"+id+"/reset-to-origin", "", alice)
		if rec.Code == http.StatusOK {
			return rec.Code, "", ""
		}
		msg, typ := e.wantErr(rec, rec.Code)
		return rec.Code, msg, typ
	}

	if code, msg, _ := reset("ws", "p1"); code != 500 || msg != "patch reset is not configured" {
		t.Errorf("no hook = %d %q", code, msg)
	}
	stub := &stubRecovery{result: ResetResult{Action: ResetActionReplaced, LocalSHA: "l1", OriginSHA: "o1", ReplacedSHA: "l1", RebuildTriggered: true, RebuildJobID: "job-1"}, sha: "l1", found: true}
	e.recovery = stub
	pre := []struct {
		name, slug, id, want, kind string
		status                     int
	}{
		{"standard", "std", "p1", "workspace is not in carry_patch mode", "", 400},
		{"archived", "archived", "p1", "workspace is archived", "", 400},
		{"clone not ready", "cloning", "p1", "workspace clone is not ready", "", 409},
		{"unknown patch", "ws", "nope", "patch not found", "", 404},
		{"integration branch", "ws", "pi", "patch branch is the integration branch", "", 400},
		{"merged", "ws", "pm", "patch status merged_upstream cannot be reset", "", 409},
	}
	for _, c := range pre {
		if code, msg, _ := reset(c.slug, c.id); code != c.status || msg != c.want {
			t.Errorf("%s: %d %q", c.name, code, msg)
		}
	}
	if len(stub.resets) != 0 {
		t.Fatal("a precondition failure reached the hook")
	}

	got := e.want(e.do(http.MethodPost, patchesPath+"/p1/reset-to-origin", "", alice), http.StatusOK)
	if got["replaced_sha"] != "l1" || got["rebuild_triggered"] != true || got["rebuild_job_id"] != "job-1" || got["branch_name"] != "feature/a" {
		t.Errorf("response = %v", got)
	}
	if len(stub.resets) != 1 || stub.resets[0] != (ResetPatchInfo{ID: "p1", BranchName: "feature/a", Status: "active", IntegrationBranch: "integration"}) {
		t.Errorf("hook received %+v", stub.resets)
	}
	resets, replaces := e.audit.ofType(EventPatchReset), e.audit.ofType(EventPatchReplace)
	if len(resets) != 1 || len(replaces) != 1 {
		t.Fatalf("events: reset=%d replace=%d", len(resets), len(replaces))
	}
	m := resets[0].Metadata
	if m["action"] != "replaced" || m["local_sha"] != "l1" || m["replaced_sha"] != "l1" || m["origin_sha"] != "o1" || resets[0].ResourceID != "feature/a" {
		t.Errorf("reset metadata = %v", m)
	}
	if replaces[0].Metadata["trigger"] != "reset_to_origin" || replaces[0].Metadata["replaced_sha"] != "l1" {
		t.Errorf("replace metadata = %v", replaces[0].Metadata)
	}

	// A created branch has no local_sha and no replace event; a disabled
	// patch is eligible; rebuild_job_id is omitted when none was enqueued.
	stub.result = ResetResult{Action: ResetActionCreated, OriginSHA: "o2"}
	stub.found = false
	got = e.want(e.do(http.MethodPost, patchesPath+"/pd/reset-to-origin", "", alice), http.StatusOK)
	if _, ok := got["rebuild_job_id"]; ok || got["rebuild_triggered"] != false {
		t.Errorf("created response = %v", got)
	}
	if _, ok := got["replaced_sha"]; ok {
		t.Error("replaced_sha present without a backup ref")
	}
	created := e.audit.ofType(EventPatchReset)[1].Metadata
	if _, ok := created["local_sha"]; ok || created["action"] != "created" || len(e.audit.ofType(EventPatchReplace)) != 1 {
		t.Errorf("created metadata = %v", created)
	}

	errs := []struct {
		err        error
		status     int
		want, kind string
	}{
		{&ResetError{Kind: ResetErrBusy}, 409, "another operation is running on this workspace; retry later", "workspace_busy"},
		{&ResetError{Kind: ResetErrMissingOnOrigin}, 409, "branch does not exist on origin", "missing_on_origin"},
		{&ResetError{Kind: ResetErrFetchFailed}, 502, "origin fetch failed", "origin_fetch_failed"},
		{&ResetError{Kind: ResetErrCredentialFailed}, 502, "failed to resolve origin credentials", ""},
		{&ResetError{Kind: ResetErrRefChanged}, 409, "patch branch changed during reset; retry", "ref_changed"},
		{&ResetError{Kind: ResetErrOther, Message: "secret detail"}, 500, "failed to update patch branch feature/a", ""},
		{errors.New("unclassified"), 500, "failed to update patch branch feature/a", ""},
	}
	before := len(e.audit.events)
	for _, c := range errs {
		stub.resetErr = c.err
		if code, msg, kind := reset("ws", "p1"); code != c.status || msg != c.want || kind != c.kind {
			t.Errorf("%v: %d %q/%q", c.err, code, msg, kind)
		}
	}
	if len(e.audit.events) != before {
		t.Error("a failed reset emitted audit events")
	}
}
