package forge

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// githubPayload is a GitHub delivery of acme/widgets from octocat, with the fields of its kind
// added.
func githubPayload(action string, fields map[string]any) string {
	doc := map[string]any{
		"repository": map[string]any{"full_name": "acme/widgets"},
		"sender":     map[string]any{"login": "octocat"},
	}
	if action != "" {
		doc["action"] = action
	}
	for k, v := range fields {
		doc[k] = v
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// pullPayload is a pull_request delivery of pull request 12 from feature to main.
func pullPayload(action string, merged bool) string {
	return githubPayload(action, map[string]any{"pull_request": map[string]any{
		"number": 12, "html_url": "https://github.com/acme/widgets/pull/12", "merged": merged,
		"head": map[string]any{"ref": "feature"}, "base": map[string]any{"ref": "main"},
	}})
}

// eventTypes lists the types of the events the emitter was given.
func eventTypes(events []audit.HubEvent) []string {
	out := []string{}
	for _, ev := range events {
		out = append(out, ev.EventType)
	}
	return out
}

// TS-32-34: The six actions of a GitHub issues delivery are the six hub.forge.issue.* events.
// Requirement: 32-REQ-6.1
func TestGitHubIssuesActions_TS_32_34(t *testing.T) {
	e, em := newIngressEnv(t)
	actions := []string{"opened", "edited", "closed", "reopened", "labeled", "unlabeled"}
	for _, action := range actions {
		d := githubDelivery(ingressSecret, "d-"+action, "issues", issuesPayload(action, "acme/widgets"))
		if outcome := outcomeOf(t, e.deliver("widgets", d)); outcome != "relayed" {
			t.Errorf("%s: outcome = %q; want relayed", action, outcome)
		}
	}
	want := []string{}
	for _, action := range actions {
		want = append(want, "hub.forge.issue."+action)
	}
	if got := eventTypes(em.recorded()); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v; want %v", got, want)
	}
	for i, ev := range em.recorded() {
		if ev.Action != actions[i] || ev.ResourceType != "issue" || ev.ResourceID != "7" {
			t.Errorf("event %d = %+v; want action %s on issue 7", i, ev, actions[i])
		}
	}
}

// TS-32-35: An issue_comment created delivery is hub.forge.comment.created, with pull_request
// true when the issue is a pull request.
// Requirement: 32-REQ-6.2
func TestGitHubIssueComment_TS_32_35(t *testing.T) {
	e, em := newIngressEnv(t)
	plain := githubPayload("created", map[string]any{
		"issue":   map[string]any{"number": 7},
		"comment": map[string]any{"html_url": "https://github.com/acme/widgets/issues/7#issuecomment-1"},
	})
	onPull := githubPayload("created", map[string]any{
		"issue":   map[string]any{"number": 12, "pull_request": map[string]any{"url": "https://api.github.com/repos/acme/widgets/pulls/12"}},
		"comment": map[string]any{"html_url": "https://github.com/acme/widgets/pull/12#issuecomment-2"},
	})
	for i, body := range []string{onPull, plain} {
		d := githubDelivery(ingressSecret, fmt.Sprintf("d-%d", i), "issue_comment", body)
		if outcome := outcomeOf(t, e.deliver("widgets", d)); outcome != "relayed" {
			t.Errorf("delivery %d: outcome = %q; want relayed", i, outcome)
		}
	}
	events := em.recorded()
	if len(events) != 2 {
		t.Fatalf("%d events; want 2", len(events))
	}
	for i, want := range []struct {
		number   int
		pull     bool
		commentU string
	}{
		{12, true, "https://github.com/acme/widgets/pull/12#issuecomment-2"},
		{7, false, "https://github.com/acme/widgets/issues/7#issuecomment-1"},
	} {
		ev := events[i]
		if ev.EventType != audit.EventForgeCommentCreated || ev.ResourceType != "comment" || ev.ResourceID != fmt.Sprint(want.number) || ev.Action != "created" {
			t.Errorf("event %d = %+v; want comment.created on %d", i, ev, want.number)
		}
		if ev.Metadata["number"] != want.number || ev.Metadata["pull_request"] != want.pull || ev.Metadata["comment_url"] != want.commentU {
			t.Errorf("event %d metadata = %v; want number %d, pull_request %v, comment_url %s", i, ev.Metadata, want.number, want.pull, want.commentU)
		}
	}
	// A comment that is edited or deleted is not a new one.
	for _, action := range []string{"edited", "deleted"} {
		d := githubDelivery(ingressSecret, "d-"+action, "issue_comment", githubPayload(action, map[string]any{"issue": map[string]any{"number": 7}}))
		if outcome := outcomeOf(t, e.deliver("widgets", d)); outcome != "ignored" {
			t.Errorf("a comment %s: outcome = %q; want ignored", action, outcome)
		}
	}
}

// TS-32-36: The pull_request actions are opened, reopened, synchronize, and merged or closed.
// Requirement: 32-REQ-6.3
func TestGitHubPullRequest_TS_32_36(t *testing.T) {
	e, em := newIngressEnv(t)
	for i, tc := range []struct {
		action string
		merged bool
	}{{"opened", false}, {"reopened", false}, {"synchronize", false}, {"closed", true}, {"closed", false}} {
		d := githubDelivery(ingressSecret, fmt.Sprintf("d-%d", i), "pull_request", pullPayload(tc.action, tc.merged))
		if outcome := outcomeOf(t, e.deliver("widgets", d)); outcome != "relayed" {
			t.Errorf("%s: outcome = %q; want relayed", tc.action, outcome)
		}
	}
	want := []string{
		"hub.forge.pull_request.opened", "hub.forge.pull_request.reopened", "hub.forge.pull_request.synchronize",
		"hub.forge.pull_request.merged", "hub.forge.pull_request.closed",
	}
	if got := eventTypes(em.recorded()); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v; want %v", got, want)
	}
	for i, ev := range em.recorded() {
		wantMeta := map[string]any{
			"forge": "github", "target": "origin", "delivery_id": fmt.Sprintf("d-%d", i), "actor": "octocat",
			"number": 12, "url": "https://github.com/acme/widgets/pull/12", "head": "feature", "base": "main",
			"merged": i == 3,
		}
		if !reflect.DeepEqual(ev.Metadata, wantMeta) {
			t.Errorf("event %d metadata = %#v\nwant %#v", i, ev.Metadata, wantMeta)
		}
		if ev.ResourceType != "pull_request" || ev.ResourceID != "12" || ev.Action != want[i][strings.LastIndex(want[i], ".")+1:] {
			t.Errorf("event %d = %+v; want pull request 12 and the action of its type", i, ev)
		}
	}
	// Another action of a pull request is not relayed.
	for _, action := range []string{"labeled", "edited", "assigned", "review_requested"} {
		d := githubDelivery(ingressSecret, "d-"+action, "pull_request", pullPayload(action, false))
		if outcome := outcomeOf(t, e.deliver("widgets", d)); outcome != "ignored" {
			t.Errorf("a pull request %s: outcome = %q; want ignored", action, outcome)
		}
	}
}

// TS-32-37: A GitHub push is hub.forge.push.
// Requirement: 32-REQ-6.4
func TestGitHubPush_TS_32_37(t *testing.T) {
	e, em := newIngressEnv(t)
	body := githubPayload("", map[string]any{"ref": "refs/heads/main", "before": "1111", "after": "2222"})
	if outcome := outcomeOf(t, e.deliver("widgets", githubDelivery(ingressSecret, "d-1", "push", body))); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	events := em.recorded()
	if got := eventTypes(events); !reflect.DeepEqual(got, []string{"hub.forge.push"}) {
		t.Fatalf("events = %v; want hub.forge.push", got)
	}
	ev := events[0]
	if ev.ResourceType != "repository" || ev.ResourceID != "refs/heads/main" || ev.Action != "push" {
		t.Errorf("event = %+v; want the repository resource, the ref as its id and the action push", ev)
	}
	want := map[string]any{"forge": "github", "target": "origin", "delivery_id": "d-1", "actor": "octocat",
		"ref": "refs/heads/main", "before": "1111", "after": "2222"}
	if !reflect.DeepEqual(ev.Metadata, want) {
		t.Errorf("metadata = %#v\nwant %#v", ev.Metadata, want)
	}
	// A push has no ref, no event.
	if rec := e.deliver("widgets", githubDelivery(ingressSecret, "d-2", "push", githubPayload("", nil))); rec.Code != 400 {
		t.Errorf("a push without a ref: status = %d; want 400", rec.Code)
	}
}

// randomText makes a string of up to max runes from ASCII, accented letters, CJK and emoji.
func randomText(r *rand.Rand, max int) string {
	pools := [][2]rune{{0x20, 0x7e}, {0xc0, 0x17f}, {0x4e00, 0x4e50}, {0x1f600, 0x1f640}, {0x10, 0x1f}}
	var b strings.Builder
	for i, n := 0, r.Intn(max+1); i < n; i++ {
		pool := pools[r.Intn(len(pools))]
		b.WriteRune(pool[0] + rune(r.Intn(int(pool[1]-pool[0]+1))))
	}
	return b.String()
}

// authorityOf returns what is between the scheme and the first slash of a URL.
func authorityOf(u string) string {
	_, rest, _ := strings.Cut(u, "://")
	authority, _, _ := strings.Cut(rest, "/")
	return authority
}

// TS-32-45: Titles are cut to 256 bytes on a UTF-8 boundary and URLs never carry user
// information, whatever the payload holds.
// Requirement: 32-REQ-8.3
func TestEventTitlesAndURLs_TS_32_45(t *testing.T) {
	e, em := newIngressEnv(t)
	r := rand.New(rand.NewSource(32))
	userinfos := []string{"user:password@", "token@", "u:p@ss@", "x-token-auth:ghp_0123456789@", ""}
	for i := 0; i < 300; i++ {
		title := randomText(r, 400)
		if i%7 == 0 { // lengths at the limit and just over it, in 1-, 2-, 3- and 4-byte characters
			title = strings.Repeat([]string{"a", "é", "世", "😀"}[i/7%4], 60+i%11)
		}
		host := userinfos[r.Intn(len(userinfos))] + "github.com/acme/widgets"
		payload := githubPayload("opened", map[string]any{"issue": map[string]any{
			"number": 7, "title": title, "html_url": "https://" + host + "/issues/7",
		}})
		if outcome := outcomeOf(t, e.deliver("widgets", githubDelivery(ingressSecret, fmt.Sprintf("d-%d", i), "issues", payload))); outcome != "relayed" {
			t.Fatalf("title %q: outcome = %q; want relayed", title, outcome)
		}
		ev := em.recorded()[i]
		got, _ := ev.Metadata["title"].(string)
		if !utf8.ValidString(got) || len(got) > 256 || !strings.HasPrefix(title, got) {
			t.Fatalf("title %q (%d bytes) became %q (%d bytes); want a valid prefix of at most 256 bytes", title, len(title), got, len(got))
		}
		if len(title) <= 256 && got != title {
			t.Fatalf("title %q of %d bytes became %q; want it kept whole", title, len(title), got)
		}
		if len(title) > 256 && len(got) < 256-3 {
			t.Fatalf("title of %d bytes became %d bytes; want the longest prefix that fits", len(title), len(got))
		}
		u, _ := ev.Metadata["url"].(string)
		if strings.Contains(authorityOf(u), "@") || u != "https://github.com/acme/widgets/issues/7" {
			t.Fatalf("url = %q; want it without user information", u)
		}
		if parsed, err := url.Parse(u); err != nil || parsed.User != nil {
			t.Fatalf("url = %q (%v); want a URL without user information", u, err)
		}
	}
}

// The URLs of a comment and of a pull request lose their user information too.
func TestEventURLsLoseUserinfo(t *testing.T) {
	e, em := newIngressEnv(t)
	comment := githubPayload("created", map[string]any{
		"issue":   map[string]any{"number": 7},
		"comment": map[string]any{"html_url": "https://user:secret@github.com/acme/widgets/issues/7#issuecomment-1"},
	})
	pull := githubPayload("opened", map[string]any{"pull_request": map[string]any{
		"number": 12, "html_url": "https://user:secret@github.com/acme/widgets/pull/12",
	}})
	labeled := strings.Replace(issuesPayload("labeled", "acme/widgets"), "https://github.com", "https://u:p@github.com", 1)
	e.deliver("widgets", githubDelivery(ingressSecret, "d-1", "issue_comment", comment))
	e.deliver("widgets", githubDelivery(ingressSecret, "d-2", "pull_request", pull))
	e.deliver("widgets", githubDelivery(ingressSecret, "d-3", "issues", labeled))
	events := em.recorded()
	if len(events) != 3 {
		t.Fatalf("%d events; want 3", len(events))
	}
	for i, key := range []string{"comment_url", "url", "url"} {
		got, _ := events[i].Metadata[key].(string)
		if strings.Contains(got, "@") || strings.Contains(got, "secret") || got == "" {
			t.Errorf("event %d %s = %q; want a URL without user information", i, key, got)
		}
	}
}

// TS-32-50: Without an emitter a delivery is still recorded and answered.
// Requirement: 32-REQ-8.8
func TestIngressWithoutAnEmitter_TS_32_50(t *testing.T) {
	e := newEnv(t, func(cfg *Config) { cfg.Audit = nil })
	e.webhookWorkspace("widgets")
	rec := e.deliver("widgets", labeledGitHubDelivery("d-1"))
	if outcome := outcomeOf(t, rec); outcome != "relayed" {
		t.Errorf("outcome = %q; want relayed", outcome)
	}
	rows := e.deliveryRows()
	if len(rows) != 1 || rows[0].outcome != "relayed" || rows[0].relayed != `["hub.forge.issue.labeled"]` {
		t.Errorf("rows = %+v; want one relayed row", rows)
	}
}

// A delivery of a kind hub relays that lacks the field which identifies what it is about is
// malformed: the number of an issue or pull request, the name of a label, the ref of a push.
// Requirement: 32-REQ-5.1
func TestGitHubDeliveriesLackingTheirIdentityAreMalformed(t *testing.T) {
	e, em := newIngressEnv(t)
	issue := func(fields map[string]any) map[string]any { return map[string]any{"issue": fields} }
	for i, tc := range []struct {
		name, event, body string
	}{
		{"an issue opened without a number", "issues", githubPayload("opened", issue(map[string]any{"title": "t"}))},
		{"an issue edited with the number 0", "issues", githubPayload("edited", issue(map[string]any{"number": 0}))},
		{"an issue closed with a number that is a string", "issues", githubPayload("closed", issue(map[string]any{"number": "7"}))},
		{"an issue labeled without a label", "issues", githubPayload("labeled", issue(map[string]any{"number": 7}))},
		{"a comment without an issue number", "issue_comment", githubPayload("created", issue(map[string]any{"title": "t"}))},
		{"a pull request opened without a pull request", "pull_request", githubPayload("opened", nil)},
		{"a pull request closed without a number", "pull_request", githubPayload("closed", map[string]any{"pull_request": map[string]any{"merged": true}})},
		{"a push without a ref", "push", githubPayload("", map[string]any{"after": "2222"})},
		{"a push with an empty ref", "push", githubPayload("", map[string]any{"ref": ""})},
		{"a push with a ref that is a number", "push", githubPayload("", map[string]any{"ref": 7})},
	} {
		rec := e.deliver("widgets", githubDelivery(ingressSecret, fmt.Sprintf("d-%d", i), tc.event, tc.body))
		if rec.Code != 400 {
			t.Errorf("%s: status = %d; want 400\n%s", tc.name, rec.Code, rec.Body)
		}
	}
	for i, row := range e.deliveryRows() {
		if row.outcome != "malformed" {
			t.Errorf("row %d outcome = %q; want malformed", i, row.outcome)
		}
	}
	if got := em.recorded(); len(got) != 0 {
		t.Errorf("events = %v; want none", got)
	}
}
