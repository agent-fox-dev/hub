package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// runAfcWithStdin is runAfc for a command that reads stdin; it returns stdout, stderr and
// the error.
func (h *forgeHub) runAfcWithStdin(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	setupTestEnv(t)
	cmd := BuildRootCommand()
	var out, errOut bytes.Buffer
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"--endpoint-url", h.URL, "--api-key", "test-api-key"}, args...))
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// jsonBody decodes the body of a request the stub hub received.
func jsonBody(t *testing.T, req forgeRequest) any {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(req.Body), &doc); err != nil {
		t.Fatalf("the body of %s %s is not JSON: %v\n%q", req.Method, req.Path, err, req.Body)
	}
	return doc
}

// assertKeyOrder checks that the keys appear in stdout in the order given.
func assertKeyOrder(t *testing.T, stdout string, keys ...string) {
	t.Helper()
	last := -1
	for _, key := range keys {
		at := strings.Index(stdout, `"`+key+`"`)
		if at <= last {
			t.Errorf("stdout = %q; want the keys in the order %v", stdout, keys)
			return
		}
		last = at
	}
}

const issueAnswer = `{"number":3,"title":"t","state":"open"}`

// TS-31-56: The write commands send the matching routes, with escaped segments, and print
// the hub's answer as one JSON document.
// Requirement: 31-REQ-10.1
func TestWriteCmdsSendTheMatchingRoutes_TS_31_56(t *testing.T) {
	const slug = "needs/escaping ?#"
	base := "/api/v1" + apiPath("workspaces", slug)
	for _, tc := range []struct {
		name   string
		args   []string
		method string
		path   string
		body   any
	}{
		{"issue create", []string{"issue", "create", slug, "--title", "t", "--body", "b", "--label", "x", "--assignee", "y"},
			http.MethodPost, base + "/issues", map[string]any{"title": "t", "body": "b", "labels": []any{"x"}, "assignees": []any{"y"}}},
		{"issue update", []string{"issue", "update", slug, "3", "--title", "t"},
			http.MethodPatch, base + "/issues/3", map[string]any{"title": "t"}},
		{"issue close with a comment", []string{"issue", "close", slug, "3", "--comment", "done"},
			http.MethodPost, base + "/issues/3/close", map[string]any{"comment": "done"}},
		{"issue close", []string{"issue", "close", slug, "3"},
			http.MethodPost, base + "/issues/3/close", nil},
		{"issue comment", []string{"issue", "comment", slug, "3", "--body", "hello"},
			http.MethodPost, base + "/issues/3/comments", map[string]any{"body": "hello"}},
		{"label create", []string{"label", "create", slug, "type/bug needs triage", "--color", "ff0000", "--description", "d"},
			http.MethodPost, base + "/labels", map[string]any{"name": "type/bug needs triage", "color": "ff0000", "description": "d"}},
		{"pr create", []string{"pr", "create", slug, "--title", "t", "--head", "me:feature", "--base", "main"},
			http.MethodPost, base + "/pulls", map[string]any{"title": "t", "head": "me:feature", "base": "main"}},
		{"pr comment", []string{"pr", "comment", slug, "3", "--body", "lgtm"},
			http.MethodPost, base + "/pulls/3/comments", map[string]any{"body": "lgtm"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newForgeHub(t, http.StatusOK, issueAnswer)
			stdout, err := hub.runAfc(t, tc.args...)
			if err != nil || ExitCode(err) != 0 {
				t.Fatalf("afc %s: %v (exit %d)", tc.name, err, ExitCode(err))
			}
			req := hub.onlyRequest(t)
			if req.Method != tc.method || req.Path != tc.path || req.RawQuery != "" {
				t.Errorf("the hub received %s %s?%s; want %s %s", req.Method, req.Path, req.RawQuery, tc.method, tc.path)
			}
			if tc.body == nil {
				if req.Body != "" {
					t.Errorf("the hub received the body %q; want none", req.Body)
				}
			} else if got := jsonBody(t, req); !reflect.DeepEqual(got, tc.body) {
				t.Errorf("the hub received %v; want %v", got, tc.body)
			}
			var want any
			_ = json.Unmarshal([]byte(issueAnswer), &want)
			if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, want) {
				t.Errorf("stdout = %v; want the hub's answer %v", got, want)
			}
		})
	}
}

// TS-31-57: label and unlabel print a status document when the hub answers 204.
// Requirement: 31-REQ-10.2
func TestLabelAndUnlabelPrintStatusDocuments_TS_31_57(t *testing.T) {
	hub := newForgeHub(t, http.StatusNoContent, ``)
	stdout, err := hub.runAfc(t, "issue", "label", "w", "3", "a", "b")
	if err != nil {
		t.Fatalf("afc issue label: %v", err)
	}
	req := hub.onlyRequest(t)
	if req.Method != http.MethodPost || req.Path != "/api/v1/workspaces/w/issues/3/labels" || req.Body != `["a","b"]` {
		t.Errorf("the hub received %+v; want POST .../issues/3/labels with the bare array [\"a\",\"b\"]", req)
	}
	if got, want := decodeOneDocument(t, stdout), any(map[string]any{"status": "labeled", "number": 3.0, "labels": []any{"a", "b"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("stdout = %v; want %v", got, want)
	}
	assertKeyOrder(t, stdout, "status", "number", "labels")

	hub = newForgeHub(t, http.StatusNoContent, ``)
	stdout, err = hub.runAfc(t, "issue", "unlabel", "w", "3", "type/bug")
	if err != nil {
		t.Fatalf("afc issue unlabel: %v", err)
	}
	req = hub.onlyRequest(t)
	if req.Method != http.MethodDelete || req.Path != "/api/v1/workspaces/w/issues/3/labels/type%2Fbug" || req.Body != "" {
		t.Errorf("the hub received %+v; want DELETE .../issues/3/labels/type%%2Fbug", req)
	}
	if got, want := decodeOneDocument(t, stdout), any(map[string]any{"status": "unlabeled", "number": 3.0, "label": "type/bug"}); !reflect.DeepEqual(got, want) {
		t.Errorf("stdout = %v; want %v", got, want)
	}
	assertKeyOrder(t, stdout, "status", "number", "label")
}

// TS-31-58: --body-file reads a file, or stdin for -, and sends the content as it is, the
// trailing newline included; a file and stdin of one content send the same bytes.
// Requirement: 31-REQ-10.3
func TestBodyFileIsSentVerbatim_TS_31_58(t *testing.T) {
	content := "# A report\r\n\r\nline two\n\n  indented\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "body.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args []string
		key  string
	}{
		{"issue create", []string{"issue", "create", "w", "--title", "t"}, "body"},
		{"issue update", []string{"issue", "update", "w", "3"}, "body"},
		{"issue comment", []string{"issue", "comment", "w", "3"}, "body"},
		{"pr create", []string{"pr", "create", "w", "--title", "t", "--head", "h", "--base", "main"}, "body"},
		{"pr comment", []string{"pr", "comment", "w", "3"}, "body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fromFile := newForgeHub(t, http.StatusOK, `{}`)
			if _, _, err := fromFile.runAfcWithStdin(t, "must not be read", append(append([]string{}, tc.args...), "--body-file", path)...); err != nil {
				t.Fatalf("--body-file <file>: %v", err)
			}
			fromStdin := newForgeHub(t, http.StatusOK, `{}`)
			if _, _, err := fromStdin.runAfcWithStdin(t, content, append(append([]string{}, tc.args...), "--body-file", "-")...); err != nil {
				t.Fatalf("--body-file -: %v", err)
			}
			a, b := jsonBody(t, fromFile.onlyRequest(t)).(map[string]any), jsonBody(t, fromStdin.onlyRequest(t)).(map[string]any)
			if a[tc.key] != content || b[tc.key] != content {
				t.Errorf("file sent %q and stdin sent %q; want both %q", a[tc.key], b[tc.key], content)
			}
		})
	}
}

// TS-31-59: A mistake the command can see is a usage error, exit 2, named in the message,
// and nothing is sent.
// Requirement: 31-REQ-10.4
func TestWriteCmdUsageErrors_TS_31_59(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-file")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"both bodies", []string{"issue", "create", "w", "--title", "t", "--body", "b", "--body-file", "-"}, "--body and --body-file"},
		{"both bodies on update", []string{"issue", "update", "w", "3", "--body", "b", "--body-file", "-"}, "--body and --body-file"},
		{"an unreadable body file", []string{"issue", "create", "w", "--title", "t", "--body-file", missing}, missing},
		{"an unreadable body file on a comment", []string{"issue", "comment", "w", "3", "--body-file", missing}, missing},
		{"no title", []string{"issue", "create", "w"}, "--title flag is required"},
		{"no pr title", []string{"pr", "create", "w", "--head", "h", "--base", "b"}, "--title flag is required"},
		{"no head", []string{"pr", "create", "w", "--title", "t", "--base", "b"}, "--head flag is required"},
		{"no base", []string{"pr", "create", "w", "--title", "t", "--head", "h"}, "--base flag is required"},
		{"no issue comment body", []string{"issue", "comment", "w", "3"}, "--body"},
		{"no pr comment body", []string{"pr", "comment", "w", "3"}, "--body"},
		{"issue number 0 on update", []string{"issue", "update", "w", "0", "--title", "t"}, "positive integer"},
		{"issue number abc on update", []string{"issue", "update", "w", "abc", "--title", "t"}, "positive integer"},
		{"issue number -1 on close", []string{"issue", "close", "w", "--", "-1"}, "positive integer"},
		{"issue number 0 on comment", []string{"issue", "comment", "w", "0", "--body", "b"}, "positive integer"},
		{"issue number abc on label", []string{"issue", "label", "w", "abc", "a"}, "positive integer"},
		{"issue number 0 on unlabel", []string{"issue", "unlabel", "w", "0", "a"}, "positive integer"},
		{"pr number abc on comment", []string{"pr", "comment", "w", "abc", "--body", "b"}, "positive integer"},
		{"an update with nothing to change", []string{"issue", "update", "w", "3"}, "nothing to update"},
		{"an update with only --target", []string{"issue", "update", "w", "3", "--target", "upstream"}, "nothing to update"},
		{"no label name", []string{"label", "create", "w", ""}, "name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newForgeHub(t, http.StatusOK, `{}`)
			stdout, _, err := hub.runAfcWithStdin(t, "stdin", tc.args...)
			if err == nil || ExitCode(err) != 2 {
				t.Fatalf("exit code = %d (%v); want 2", ExitCode(err), err)
			}
			if !strings.Contains(stdout, tc.want) {
				t.Errorf("stdout = %q; want it to name %q", stdout, tc.want)
			}
			if reqs := hub.received(); len(reqs) != 0 {
				t.Errorf("the hub received %+v; want no request", reqs)
			}
		})
	}
}

// TS-31-60: Only the flags that are set are sent: an empty --body clears the body, a label
// with a comma stays one label, --target is a query parameter only when it is given.
// Requirement: 31-REQ-10.5
func TestOnlySetFlagsAreSent_TS_31_60(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		body  any
		query string
	}{
		{"an empty body", []string{"issue", "update", "w", "3", "--body", ""}, map[string]any{"body": ""}, ""},
		{"labels and assignees", []string{"issue", "update", "w", "3", "--label", "a,b", "--assignee", "x", "--assignee", "y"},
			map[string]any{"labels": []any{"a,b"}, "assignees": []any{"x", "y"}}, ""},
		{"a state", []string{"issue", "update", "w", "3", "--state", "closed"}, map[string]any{"state": "closed"}, ""},
		{"the target", []string{"issue", "create", "w", "--title", "t", "--target", "upstream"}, map[string]any{"title": "t"}, "target=upstream"},
		{"a draft", []string{"pr", "create", "w", "--title", "t", "--head", "h", "--base", "b", "--draft"},
			map[string]any{"title": "t", "head": "h", "base": "b", "draft": true}, ""},
		{"a draft turned off", []string{"pr", "create", "w", "--title", "t", "--head", "h", "--base", "b", "--draft=false"},
			map[string]any{"title": "t", "head": "h", "base": "b", "draft": false}, ""},
		{"a label with no color", []string{"label", "create", "w", "af:triage", "--target", "origin"}, map[string]any{"name": "af:triage"}, "target=origin"},
		{"a close on the upstream", []string{"issue", "close", "w", "3", "--target", "upstream"}, nil, "target=upstream"},
		{"labels on the upstream", []string{"issue", "label", "w", "3", "a", "--target", "upstream"}, []any{"a"}, "target=upstream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newForgeHub(t, http.StatusOK, `{}`)
			if _, err := hub.runAfc(t, tc.args...); err != nil {
				t.Fatalf("afc: %v", err)
			}
			req := hub.onlyRequest(t)
			if req.RawQuery != tc.query || (tc.query == "" && strings.Contains(req.RequestURI, "?")) {
				t.Errorf("query = %q (%s); want %q", req.RawQuery, req.RequestURI, tc.query)
			}
			if tc.body == nil {
				if req.Body != "" {
					t.Errorf("body = %q; want none", req.Body)
				}
			} else if got := jsonBody(t, req); !reflect.DeepEqual(got, tc.body) {
				t.Errorf("body = %v; want %v", got, tc.body)
			}
		})
	}

	// The help of update says that labels and assignees replace the issue's set.
	hub := newForgeHub(t, http.StatusOK, `{}`)
	stdout, stderr, err := hub.runAfcWithStdin(t, "", "issue", "update", "--help")
	if err != nil {
		t.Fatal(err)
	}
	help := stdout + stderr
	for _, flag := range []string{"--label", "--assignee"} {
		line := ""
		for _, l := range strings.Split(help, "\n") {
			if strings.Contains(l, flag) {
				line = l
			}
		}
		if !strings.Contains(line, "replace") {
			t.Errorf("the help of %s on update is %q; want it to say that it replaces the set", flag, line)
		}
	}
}

// TS-31-61: A hub error goes through CLIHandleError with apikit's exit code: the error
// envelope on stdout and no result document.
// Requirement: 31-REQ-10.6
func TestWriteCmdHubErrors_TS_31_61(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		args   []string
	}{
		{"413 body_too_large", 413, `{"error":{"code":413,"message":"body is 70000 characters; github accepts at most 65536","error_type":"body_too_large"}}`,
			[]string{"issue", "create", "w", "--title", "t", "--body", "x"}},
		{"404 forge_not_found", 404, `{"error":{"code":404,"message":"the forge answered 404: Not Found","error_type":"forge_not_found"}}`,
			[]string{"issue", "unlabel", "w", "99", "af:fix"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newForgeHub(t, tc.status, tc.body)
			stdout, err := hub.runAfc(t, tc.args...)
			if err == nil {
				t.Fatal("the command succeeded on an error status")
			}
			if code := ExitCode(err); code != 1 {
				t.Errorf("exit code = %d; want apikit's 1 for an API error", code)
			}
			var doc map[string]map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil || doc["error"] == nil || len(doc) != 1 {
				t.Fatalf("stdout = %q; want the error envelope alone (%v)", stdout, err)
			}
			var want map[string]map[string]any
			_ = json.Unmarshal([]byte(tc.body), &want)
			if doc["error"]["message"] != want["error"]["message"] {
				t.Errorf("message = %v; want the hub's %v", doc["error"]["message"], want["error"]["message"])
			}
		})
	}
}
