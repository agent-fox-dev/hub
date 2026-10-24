package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const (
	webhookAnswer          = `{"configured":true,"url":"https://hub.example.com/hooks/forge/widgets"}`
	webhookGeneratedAnswer = `{"configured":true,"url":"https://hub.example.com/hooks/forge/widgets","secret":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`
	webhookPath            = "/api/v1/workspaces/widgets/forge/webhook"
)

// TS-32-57: afc forge webhook set --secret sends the secret and prints the hub's answer as one
// JSON document.
// Requirement: 32-REQ-10.1
func TestWebhookSetWithASecret_TS_32_57(t *testing.T) {
	hub := newForgeHub(t, http.StatusOK, webhookAnswer)
	stdout, stderr, err := hub.runAfcWithStdin(t, "", "forge", "webhook", "set", "widgets", "--secret", "s3cretvalue123456")
	if err != nil || ExitCode(err) != 0 {
		t.Fatalf("afc forge webhook set: %v (exit %d)", err, ExitCode(err))
	}
	req := hub.onlyRequest(t)
	if req.Method != http.MethodPut || req.Path != webhookPath || req.RawQuery != "" {
		t.Errorf("the hub received %s %s?%s; want PUT %s", req.Method, req.Path, req.RawQuery, webhookPath)
	}
	if got, want := jsonBody(t, req), any(map[string]any{"secret": "s3cretvalue123456"}); !reflect.DeepEqual(got, want) {
		t.Errorf("the hub received %v; want %v", got, want)
	}
	var want any
	_ = json.Unmarshal([]byte(webhookAnswer), &want)
	if got := decodeOneDocument(t, stdout); !reflect.DeepEqual(got, want) {
		t.Errorf("stdout = %v; want the hub's answer %v", got, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q; want nothing: the secret was supplied", stderr)
	}
}

// TS-32-58: set with --generate or with no flag sends {} and says on stderr that the secret is not
// shown again; --from-stdin sends what stdin holds minus one trailing newline.
// Requirement: 32-REQ-10.2, 32-REQ-10.3
func TestWebhookSetGenerateAndStdin_TS_32_58(t *testing.T) {
	for _, args := range [][]string{
		{"forge", "webhook", "set", "widgets", "--generate"},
		{"forge", "webhook", "set", "widgets"},
	} {
		hub := newForgeHub(t, http.StatusOK, webhookGeneratedAnswer)
		stdout, stderr, err := hub.runAfcWithStdin(t, "", args...)
		if err != nil {
			t.Fatalf("afc %v: %v", args, err)
		}
		req := hub.onlyRequest(t)
		if req.Method != http.MethodPut || req.Path != webhookPath || strings.TrimSpace(req.Body) != "{}" {
			t.Errorf("afc %v: the hub received %s %s %q; want PUT %s with {}", args, req.Method, req.Path, req.Body, webhookPath)
		}
		doc, _ := decodeOneDocument(t, stdout).(map[string]any)
		if doc["secret"] == nil || doc["configured"] != true {
			t.Errorf("afc %v: stdout = %s; want the document with the secret", args, stdout)
		}
		lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
		if len(lines) != 1 || !strings.Contains(lines[0], "again") {
			t.Errorf("afc %v: stderr = %q; want one line that says hub will not show the secret again", args, stderr)
		}
		if strings.Contains(stderr, "0123456789abcdef") {
			t.Errorf("afc %v: stderr holds the secret: %q", args, stderr)
		}
	}

	hub := newForgeHub(t, http.StatusOK, webhookAnswer)
	_, stderr, err := hub.runAfcWithStdin(t, "topsecretvalue-1234\n\n", "forge", "webhook", "set", "widgets", "--from-stdin")
	if err != nil {
		t.Fatalf("afc forge webhook set --from-stdin: %v", err)
	}
	req := hub.onlyRequest(t)
	if got, want := jsonBody(t, req), any(map[string]any{"secret": "topsecretvalue-1234\n"}); !reflect.DeepEqual(got, want) {
		t.Errorf("--from-stdin: the hub received %v; want one trailing newline dropped: %v", got, want)
	}
	if stderr != "" {
		t.Errorf("--from-stdin: stderr = %q; want nothing", stderr)
	}
}

// TS-32-59: --secret, --from-stdin and --generate exclude each other: exit 2, and nothing is sent.
// Requirement: 32-REQ-10.4
func TestWebhookSetFlagsExcludeEachOther_TS_32_59(t *testing.T) {
	for _, flags := range [][]string{
		{"--secret", "s3cretvalue123456", "--from-stdin"},
		{"--secret", "s3cretvalue123456", "--generate"},
		{"--from-stdin", "--generate"},
		{"--secret", "s3cretvalue123456", "--from-stdin", "--generate"},
	} {
		hub := newForgeHub(t, http.StatusOK, webhookAnswer)
		stdout, _, err := hub.runAfcWithStdin(t, "from stdin", append([]string{"forge", "webhook", "set", "widgets"}, flags...)...)
		if err == nil || ExitCode(err) != 2 {
			t.Errorf("%v: exit code = %d (%v); want 2", flags, ExitCode(err), err)
		}
		if !strings.Contains(stdout, "mutually exclusive") {
			t.Errorf("%v: stdout = %q; want it to say the flags are mutually exclusive", flags, stdout)
		}
		if reqs := hub.received(); len(reqs) != 0 {
			t.Errorf("%v: the hub received %+v; want no request", flags, reqs)
		}
	}
}

// TS-32-60: afc forge webhook unset sends DELETE and prints a status document.
// Requirement: 32-REQ-10.5
func TestWebhookUnset_TS_32_60(t *testing.T) {
	hub := newForgeHub(t, http.StatusNoContent, ``)
	stdout, err := hub.runAfc(t, "forge", "webhook", "unset", "widgets")
	if err != nil || ExitCode(err) != 0 {
		t.Fatalf("afc forge webhook unset: %v (exit %d)", err, ExitCode(err))
	}
	req := hub.onlyRequest(t)
	if req.Method != http.MethodDelete || req.Path != webhookPath || req.Body != "" {
		t.Errorf("the hub received %+v; want DELETE %s", req, webhookPath)
	}
	if got, want := decodeOneDocument(t, stdout), any(map[string]any{"status": "unset", "workspace": "widgets"}); !reflect.DeepEqual(got, want) {
		t.Errorf("stdout = %v; want %v", got, want)
	}
	assertKeyOrder(t, stdout, "status", "workspace")
}

// TS-32-61: afc forge deliveries sends only the flags that are set, as given, and prints the answer.
// Requirement: 32-REQ-10.6
func TestDeliveriesSendsOnlyTheFlagsSet_TS_32_61(t *testing.T) {
	const answer = `[{"delivery_id":"d-1","forge":"github","event":"issues","action":"labeled","target":"origin","outcome":"relayed","relayed":["hub.forge.issue.labeled"],"received_at":"2026-10-21T09:12:03Z"}]`
	for _, tc := range []struct {
		args []string
		want url.Values
	}{
		{nil, url.Values{}},
		{[]string{"--limit", "10"}, url.Values{"limit": {"10"}}},
		{[]string{"--since", "2026-10-21T00:00:00Z"}, url.Values{"since": {"2026-10-21T00:00:00Z"}}},
		{[]string{"--limit", "500"}, url.Values{"limit": {"500"}}},
		{[]string{"--since", "yesterday", "--limit", "0"}, url.Values{"since": {"yesterday"}, "limit": {"0"}}},
		{[]string{"--limit", ""}, url.Values{"limit": {""}}},
		{[]string{"--since", ""}, url.Values{"since": {""}}},
	} {
		hub := newForgeHub(t, http.StatusOK, answer)
		stdout, err := hub.runAfc(t, append([]string{"forge", "deliveries", "widgets"}, tc.args...)...)
		if err != nil {
			t.Fatalf("afc forge deliveries %v: %v", tc.args, err)
		}
		req := hub.onlyRequest(t)
		got, perr := url.ParseQuery(req.RawQuery)
		if req.Method != http.MethodGet || req.Path != "/api/v1/workspaces/widgets/forge/deliveries" || perr != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("afc forge deliveries %v: the hub received %s %s?%s; want GET .../forge/deliveries with the query %v", tc.args, req.Method, req.Path, req.RawQuery, tc.want)
		}
		if len(tc.want) == 0 && strings.Contains(req.RequestURI, "?") {
			t.Errorf("afc forge deliveries: the request line is %s; want no query at all", req.RequestURI)
		}
		var want any
		_ = json.Unmarshal([]byte(answer), &want)
		if doc := decodeOneDocument(t, stdout); !reflect.DeepEqual(doc, want) {
			t.Errorf("afc forge deliveries %v: stdout = %v; want %v", tc.args, doc, want)
		}
	}
}

// TS-32-62: A hub error goes through CLIHandleError: apikit's exit code, the error envelope and the
// hub's message.
// Requirement: 32-REQ-10.7
func TestWebhookCmdsReportHubErrors_TS_32_62(t *testing.T) {
	commands := map[string][]string{
		"set":        {"forge", "webhook", "set", "widgets", "--secret", "s3cretvalue123456"},
		"unset":      {"forge", "webhook", "unset", "widgets"},
		"deliveries": {"forge", "deliveries", "widgets"},
	}
	for name, args := range commands {
		for _, status := range []int{400, 401, 403, 404, 500} {
			message := fmt.Sprintf("the hub says no (%d)", status)
			hub := newForgeHub(t, status, fmt.Sprintf(`{"error":{"code":%d,"message":%q}}`, status, message))
			stdout, err := hub.runAfc(t, args...)
			if err == nil {
				t.Errorf("%s with %d: the command succeeded", name, status)
				continue
			}
			if code := ExitCode(err); code != 1 {
				t.Errorf("%s with %d: exit code = %d; want apikit's 1 for an API error", name, status, code)
			}
			var doc map[string]map[string]any
			if jerr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); jerr != nil || len(doc) != 1 ||
				doc["error"]["message"] != message || doc["error"]["code"] != float64(status) {
				t.Errorf("%s with %d: stdout = %q; want the hub's error envelope alone (%v)", name, status, stdout, jerr)
			}
		}
	}
}

// findCommand walks the command tree from root along the names.
func findCommand(root *cobra.Command, names ...string) *cobra.Command {
	cmd := root
	for _, name := range names {
		var next *cobra.Command
		for _, sub := range cmd.Commands() {
			if sub.Name() == name {
				next = sub
			}
		}
		if next == nil {
			return nil
		}
		cmd = next
	}
	return cmd
}

// TS-32-63: The subcommands are under afc forge, and a slug is escaped in the path.
// Requirement: 32-REQ-10.8
func TestForgeSubcommandsAreRegistered_TS_32_63(t *testing.T) {
	root := BuildRootCommand()
	for _, names := range [][]string{{"forge", "webhook", "set"}, {"forge", "webhook", "unset"}, {"forge", "deliveries"}, {"forge", "status"}} {
		if findCommand(root, names...) == nil {
			t.Errorf("afc %s is not registered", strings.Join(names, " "))
		}
	}

	hub := newForgeHub(t, http.StatusNoContent, ``)
	if _, err := hub.runAfc(t, "forge", "webhook", "unset", "a/b c?"); err != nil {
		t.Fatalf("afc forge webhook unset: %v", err)
	}
	if req := hub.onlyRequest(t); req.Path != "/api/v1/workspaces/a%2Fb%20c%3F/forge/webhook" {
		t.Errorf("the hub received the path %s; want the slug escaped", req.Path)
	}
}
