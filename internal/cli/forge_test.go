package cli

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	forgeStatus    = `{"forge":"github","api_url":"https://api.github.com","repository":{"owner":"acme","name":"widgets"},"credential":"pat","upstream":null}`
	webhookAnswer  = `{"url":"https://hub.example/webhooks/forge/widgets","configured":true}`
	webhookCreated = `{"url":"https://hub.example/webhooks/forge/widgets","configured":true,"secret":"0123456789abcdef"}`
)

// query parses the query string of a request.
func query(t *testing.T, r request) url.Values {
	t.Helper()
	q, err := url.ParseQuery(r.Query)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// The reads send GET to their routes, the slug and the number escaped as
// path segments, --target as a query parameter only when given, and print
// the hub's answer as one document.
func TestForgeReads_SendTheirRoutes(t *testing.T) {
	const slug = "needs/escaping ?#"
	base := "/api/v1" + apiPath("workspaces", slug)
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"forge", "status", slug}, base + "/forge"},
		{[]string{"forge", "deliveries", slug}, base + "/forge/deliveries"},
		{[]string{"issue", "list", slug}, base + "/issues"},
		{[]string{"issue", "get", slug, "42"}, base + "/issues/42"},
		{[]string{"pr", "get", slug, "42"}, base + "/pulls/42"},
		{[]string{"pr", "state", slug, "1/../2"}, base + "/pulls/1%2F..%2F2/state"},
		{[]string{"pr", "files", slug, "42"}, base + "/pulls/42/files"},
		{[]string{"pr", "checks", slug, "42"}, base + "/pulls/42/checks"},
		{[]string{"pr", "reviews", slug, "42"}, base + "/pulls/42/reviews"},
	} {
		t.Run(strings.Join(tc.args[:2], " "), func(t *testing.T) {
			h := newHub(t, http.StatusOK, forgeStatus)
			stdout, _, err := h.run(t, tc.args...)
			if err != nil {
				t.Fatalf("afc %v: %v", tc.args, err)
			}
			if req := h.only(t); req.Method != http.MethodGet || req.Path != tc.path || req.Query != "" {
				t.Errorf("request = %+v; want GET %s with no query", req, tc.path)
			}
			if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, decodeJSON(t, forgeStatus)) {
				t.Errorf("stdout = %q; want the hub's answer", stdout)
			}
			if tc.args[0] == "forge" {
				return
			}
			h = newHub(t, http.StatusOK, `{}`)
			if _, _, err := h.run(t, append(tc.args, "--target", "upstream")...); err != nil {
				t.Fatalf("afc %v --target: %v", tc.args, err)
			}
			if req := h.only(t); req.Query != "target=upstream" {
				t.Errorf("with --target: query = %q; want target=upstream", req.Query)
			}
		})
	}
}

// issue list and forge deliveries send each flag that is set as a query
// parameter, as given, even empty; repeated --label values are joined.
func TestForgeLists_SendTheFlagsSetAsQuery(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want url.Values
	}{
		{[]string{"issue", "list", "w", "--label", "a", "--label", "b,c", "--state", "open", "--assignee", "bob", "--limit", "5", "--target", "upstream"},
			url.Values{"labels": {"a,b,c"}, "state": {"open"}, "assignee": {"bob"}, "limit": {"5"}, "target": {"upstream"}}},
		{[]string{"issue", "list", "w", "--limit", "abc", "--state", ""}, url.Values{"limit": {"abc"}, "state": {""}}},
		{[]string{"forge", "deliveries", "w", "--since", "2026-10-21T00:00:00Z", "--limit", "10"}, url.Values{"since": {"2026-10-21T00:00:00Z"}, "limit": {"10"}}},
		{[]string{"forge", "deliveries", "w", "--limit", ""}, url.Values{"limit": {""}}},
	} {
		h := newHub(t, http.StatusOK, `[]`)
		if _, _, err := h.run(t, tc.args...); err != nil {
			t.Fatalf("afc %v: %v", tc.args, err)
		}
		if got := query(t, h.only(t)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("afc %v: query = %v; want %v", tc.args, got, tc.want)
		}
	}
}

// The writes send their routes and bodies, with escaped segments, and print
// the hub's answer; label and unlabel print a status document, since the hub
// answers 204.
func TestForgeWrites_SendTheirRoutes(t *testing.T) {
	const slug = "needs/escaping ?#"
	base := "/api/v1" + apiPath("workspaces", slug)
	for _, tc := range []struct {
		name         string
		args         []string
		method, path string
		body         any
		stdout       any
	}{
		{"issue create", []string{"issue", "create", slug, "--title", "t", "--body", "b", "--label", "x", "--assignee", "y"},
			http.MethodPost, base + "/issues", map[string]any{"title": "t", "body": "b", "labels": []any{"x"}, "assignees": []any{"y"}}, nil},
		{"issue update", []string{"issue", "update", slug, "3", "--title", "t", "--body", "", "--state", "closed", "--label", "a,b", "--assignee", "x", "--assignee", "y"},
			http.MethodPatch, base + "/issues/3", map[string]any{"title": "t", "body": "", "state": "closed", "labels": []any{"a,b"}, "assignees": []any{"x", "y"}}, nil},
		{"issue close with a comment", []string{"issue", "close", slug, "3", "--comment", "done"},
			http.MethodPost, base + "/issues/3/close", map[string]any{"comment": "done"}, nil},
		{"issue close", []string{"issue", "close", slug, "3"}, http.MethodPost, base + "/issues/3/close", nil, nil},
		{"issue comment", []string{"issue", "comment", slug, "3", "--body", "hello"},
			http.MethodPost, base + "/issues/3/comments", map[string]any{"body": "hello"}, nil},
		{"issue label", []string{"issue", "label", slug, "3", "a", "b"},
			http.MethodPost, base + "/issues/3/labels", []any{"a", "b"}, map[string]any{"status": "labeled", "number": 3.0, "labels": []any{"a", "b"}}},
		{"issue unlabel", []string{"issue", "unlabel", slug, "3", "type/bug"},
			http.MethodDelete, base + "/issues/3/labels/type%2Fbug", nil, map[string]any{"status": "unlabeled", "number": 3.0, "label": "type/bug"}},
		{"label create", []string{"label", "create", slug, "type/bug needs triage", "--color", "ff0000", "--description", "d"},
			http.MethodPost, base + "/labels", map[string]any{"name": "type/bug needs triage", "color": "ff0000", "description": "d"}, nil},
		{"pr create", []string{"pr", "create", slug, "--title", "t", "--head", "me:feature", "--base", "main", "--draft=false"},
			http.MethodPost, base + "/pulls", map[string]any{"title": "t", "head": "me:feature", "base": "main", "draft": false}, nil},
		{"pr comment", []string{"pr", "comment", slug, "3", "--body", "lgtm"},
			http.MethodPost, base + "/pulls/3/comments", map[string]any{"body": "lgtm"}, nil},
		{"webhook set", []string{"forge", "webhook", "set", slug, "--secret", "s3cretvalue123456"},
			http.MethodPut, base + "/forge/webhook", map[string]any{"secret": "s3cretvalue123456"}, nil},
		{"webhook unset", []string{"forge", "webhook", "unset", slug},
			http.MethodDelete, base + "/forge/webhook", nil, map[string]any{"status": "unset", "workspace": slug}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, answer := http.StatusOK, webhookAnswer
			if tc.stdout != nil {
				status, answer = http.StatusNoContent, ""
			}
			h := newHub(t, status, answer)
			stdout, stderr, err := h.run(t, tc.args...)
			if err != nil {
				t.Fatalf("afc %s: %v", tc.name, err)
			}
			req := h.only(t)
			if req.Method != tc.method || req.Path != tc.path || req.Query != "" {
				t.Errorf("request = %s %s?%s; want %s %s", req.Method, req.Path, req.Query, tc.method, tc.path)
			}
			if tc.body == nil && req.Body != "" || tc.body != nil && !reflect.DeepEqual(decodeJSON(t, req.Body), tc.body) {
				t.Errorf("body = %q; want %v", req.Body, tc.body)
			}
			want := tc.stdout
			if want == nil {
				want = decodeJSON(t, answer)
			}
			if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, want) || stderr != "" {
				t.Errorf("stdout = %q, stderr = %q; want %v and nothing on stderr", stdout, stderr, want)
			}
		})
	}
}

// --target becomes a query parameter of a write only when given; --draft is
// sent only when set.
func TestForgeWrites_SendTargetAndDraftOnlyWhenGiven(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		body  any
		query string
	}{
		{[]string{"issue", "create", "w", "--title", "t", "--target", "upstream"}, map[string]any{"title": "t"}, "target=upstream"},
		{[]string{"pr", "create", "w", "--title", "t", "--head", "h", "--base", "b", "--draft"}, map[string]any{"title": "t", "head": "h", "base": "b", "draft": true}, ""},
		{[]string{"label", "create", "w", "af:triage", "--target", "origin"}, map[string]any{"name": "af:triage"}, "target=origin"},
		{[]string{"issue", "close", "w", "3", "--target", "upstream"}, nil, "target=upstream"},
		{[]string{"issue", "label", "w", "3", "a", "--target", "upstream"}, []any{"a"}, "target=upstream"},
		{[]string{"issue", "unlabel", "w", "3", "a", "--target", "upstream"}, nil, "target=upstream"},
		{[]string{"pr", "comment", "w", "3", "--body", "b", "--target", "upstream"}, map[string]any{"body": "b"}, "target=upstream"},
	} {
		h := newHub(t, http.StatusOK, `{}`)
		if _, _, err := h.run(t, tc.args...); err != nil {
			t.Fatalf("afc %v: %v", tc.args, err)
		}
		req := h.only(t)
		if req.Query != tc.query || tc.body == nil && req.Body != "" || tc.body != nil && !reflect.DeepEqual(decodeJSON(t, req.Body), tc.body) {
			t.Errorf("afc %v: request = %+v; want query %q and body %v", tc.args, req, tc.query, tc.body)
		}
	}
}

// --body-file sends a file, or stdin for -, as it is, trailing newline
// included; --body is given when the file is.
func TestForgeWrites_BodyFileIsSentVerbatim(t *testing.T) {
	content := "# A report\r\n\r\nline two\n\n  indented\n"
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"issue", "create", "w", "--title", "t"}, {"issue", "update", "w", "3"}, {"issue", "comment", "w", "3"},
		{"pr", "create", "w", "--title", "t", "--head", "h", "--base", "main"}, {"pr", "comment", "w", "3"},
	} {
		fromFile := newHub(t, http.StatusOK, `{}`)
		if _, _, err := fromFile.runStdin(t, "must not be read", append(args, "--body-file", path)...); err != nil {
			t.Fatalf("afc %v --body-file <file>: %v", args, err)
		}
		fromStdin := newHub(t, http.StatusOK, `{}`)
		if _, _, err := fromStdin.runStdin(t, content, append(args, "--body-file", "-")...); err != nil {
			t.Fatalf("afc %v --body-file -: %v", args, err)
		}
		a, _ := decodeJSON(t, fromFile.only(t).Body).(map[string]any)
		b, _ := decodeJSON(t, fromStdin.only(t).Body).(map[string]any)
		if a["body"] != content || b["body"] != content {
			t.Errorf("afc %v: file sent %q and stdin sent %q; want both %q", args, a["body"], b["body"], content)
		}
	}
}

// webhook set with --generate or no flag sends {} and says on stderr that the
// secret is shown once; --from-stdin sends stdin minus one trailing newline.
func TestWebhookSet_GeneratesOrReadsStdin(t *testing.T) {
	for _, args := range [][]string{{"--generate"}, nil} {
		h := newHub(t, http.StatusOK, webhookCreated)
		stdout, stderr, err := h.run(t, append([]string{"forge", "webhook", "set", "w"}, args...)...)
		if err != nil {
			t.Fatalf("webhook set %v: %v", args, err)
		}
		if req := h.only(t); req.Method != http.MethodPut || strings.TrimSpace(req.Body) != "{}" {
			t.Errorf("webhook set %v: request = %+v; want PUT with {}", args, req)
		}
		if doc, _ := decodeOneDocument(t, stdout).(map[string]any); doc["secret"] != "0123456789abcdef" {
			t.Errorf("webhook set %v: stdout = %q; want the generated secret", args, stdout)
		}
		if lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "not show this secret again") || strings.Contains(stderr, "0123456789abcdef") {
			t.Errorf("webhook set %v: stderr = %q; want one note without the secret", args, stderr)
		}
	}

	h := newHub(t, http.StatusOK, webhookAnswer)
	_, stderr, err := h.runStdin(t, "topsecretvalue-1234\n\n", "forge", "webhook", "set", "w", "--from-stdin")
	if err != nil {
		t.Fatalf("webhook set --from-stdin: %v", err)
	}
	if req := h.only(t); req.Body != `{"secret":"topsecretvalue-1234\n"}` || stderr != "" {
		t.Errorf("body = %q, stderr = %q; want one trailing newline dropped and no note", req.Body, stderr)
	}
}

// A mistake the command can see is a usage error, exit 2, named in the
// message, and nothing is sent.
func TestForge_UsageErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-file")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"issue", "create", "w", "--title", "t", "--body", "b", "--body-file", "-"}, "--body and --body-file"},
		{[]string{"issue", "create", "w", "--title", "t", "--body-file", missing}, missing},
		{[]string{"issue", "create", "w"}, "--title flag is required"},
		{[]string{"pr", "create", "w", "--head", "h", "--base", "b"}, "--title flag is required"},
		{[]string{"pr", "create", "w", "--title", "t", "--base", "b"}, "--head flag is required"},
		{[]string{"pr", "create", "w", "--title", "t", "--head", "h"}, "--base flag is required"},
		{[]string{"pr", "create", "w", "--title", "t", "--head", "h", "--base", "b", "--body-file", missing}, missing},
		{[]string{"issue", "comment", "w", "3"}, "--body or --body-file flag is required"},
		{[]string{"pr", "comment", "w", "3", "--body-file", missing}, missing},
		{[]string{"issue", "update", "w", "0", "--title", "t"}, "positive integer"},
		{[]string{"issue", "update", "w", "3", "--body", "b", "--body-file", "-"}, "--body and --body-file"},
		{[]string{"issue", "close", "w", "--", "-1"}, "positive integer"},
		{[]string{"issue", "comment", "w", "abc", "--body", "b"}, "the issue number must be a positive integer"},
		{[]string{"pr", "comment", "w", "abc", "--body", "b"}, "the pull request number must be a positive integer"},
		{[]string{"issue", "label", "w", "abc", "a"}, "positive integer"},
		{[]string{"issue", "unlabel", "w", "0", "a"}, "positive integer"},
		{[]string{"issue", "update", "w", "3", "--target", "upstream"}, "nothing to update"},
		{[]string{"label", "create", "w", ""}, "the label name must not be empty"},
		{[]string{"forge", "webhook", "set", "w", "--secret", "s3cretvalue123456", "--from-stdin"}, "mutually exclusive"},
		{[]string{"forge", "webhook", "set", "w", "--from-stdin", "--generate"}, "mutually exclusive"},
		{[]string{"forge", "status"}, ""},
		{[]string{"forge", "deliveries", "w", "x"}, ""},
		{[]string{"issue", "get", "w"}, ""},
		{[]string{"issue", "label", "w", "3"}, ""},
		{[]string{"pr", "reviews", "w", "1", "extra"}, ""},
		{[]string{"forge", "webhook", "unset"}, ""},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			h := newHub(t, http.StatusOK, `{}`)
			stdout, _, err := h.runStdin(t, "stdin", tc.args...)
			assertUsageError(t, h, stdout, err, tc.want)
		})
	}
}

// A hub error exits 1 with the hub's envelope, message included, as the only
// document on stdout.
func TestForge_HubErrorsAreReported(t *testing.T) {
	for _, tc := range []struct {
		status int
		args   []string
	}{
		{404, []string{"issue", "get", "w", "7"}},
		{429, []string{"forge", "status", "w"}},
		{413, []string{"issue", "create", "w", "--title", "t", "--body", "x"}},
		{404, []string{"issue", "unlabel", "w", "99", "af:fix"}},
		{400, []string{"forge", "webhook", "set", "w", "--secret", "s3cretvalue123456"}},
		{500, []string{"forge", "webhook", "unset", "w"}},
		{403, []string{"forge", "deliveries", "w"}},
	} {
		h := newHub(t, tc.status, errorBody(tc.status, "the forge says no"))
		stdout, _, err := h.run(t, tc.args...)
		assertEnvelopeOnly(t, stdout, err, "the forge says no")
	}
}
