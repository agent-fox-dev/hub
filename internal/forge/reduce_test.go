package forge

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agent-fox-dev/hub/internal/audit"
)

var widgetsRemotes = workspaceRemotes{gitURL: "https://github.com/acme/widgets", mode: "standard"}

// reduce reduces a verified delivery of acme/widgets.
func reduce(forge, event, body string) reduction {
	return reduceDelivery(delivery{forge: forge, slug: "widgets", id: "d-1", event: event, body: []byte(body)}, widgetsRemotes)
}

// relayed reduces a delivery that must relay and returns its events.
func relayed(t *testing.T, forge, event, body string) []audit.HubEvent {
	t.Helper()
	red := reduce(forge, event, body)
	if red.outcome != outcomeRelayed {
		t.Fatalf("%s %s: outcome = %q; want relayed", forge, event, red.outcome)
	}
	return red.events
}

// The GitHub deliveries hub relays and the events they become, with their metadata;
// other actions and kinds are ignored; a delivery lacking the field that identifies what
// it is about is malformed.
func TestReduce_GitHub(t *testing.T) {
	base := map[string]any{"forge": "github", "target": "origin", "delivery_id": "d-1", "actor": "octocat"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	issue := map[string]any{"number": 7, "title": "Widgets break", "url": "https://github.com/acme/widgets/issues/7", "labels": []string{"bug", "af:ready"}}
	label := map[string]any{"number": 7, "label": "af:ready", "url": "https://github.com/acme/widgets/issues/7", "labels": []string{"bug", "af:ready"}}
	pull := func(merged bool) map[string]any {
		return map[string]any{"number": 12, "url": "https://github.com/acme/widgets/pull/12", "head": "feature", "base": "main", "merged": merged}
	}
	for _, tc := range []struct {
		event, body                     string
		eventType, resource, resourceID string
		metadata                        map[string]any
	}{
		{"issues", issuesPayload("opened", "acme/widgets"), audit.EventForgeIssueOpened, "issue", "7", issue},
		{"issues", issuesPayload("edited", "acme/widgets"), audit.EventForgeIssueEdited, "issue", "7", issue},
		{"issues", issuesPayload("closed", "acme/widgets"), audit.EventForgeIssueClosed, "issue", "7", issue},
		{"issues", issuesPayload("reopened", "acme/widgets"), audit.EventForgeIssueReopened, "issue", "7", issue},
		{"issues", issuesPayload("labeled", "acme/widgets"), audit.EventForgeIssueLabeled, "issue", "7", label},
		{"issues", issuesPayload("unlabeled", "acme/widgets"), audit.EventForgeIssueUnlabeled, "issue", "7", label},
		{"issue_comment", githubPayload("created", map[string]any{"issue": map[string]any{"number": 7}, "comment": map[string]any{"html_url": "https://u:p@github.com/acme/widgets/issues/7#issuecomment-1"}}),
			audit.EventForgeCommentCreated, "comment", "7", map[string]any{"number": 7, "comment_url": "https://github.com/acme/widgets/issues/7#issuecomment-1", "pull_request": false}},
		{"issue_comment", githubPayload("created", map[string]any{"issue": map[string]any{"number": 12, "pull_request": map[string]any{"url": "x"}}, "comment": map[string]any{}}),
			audit.EventForgeCommentCreated, "comment", "12", map[string]any{"number": 12, "comment_url": "", "pull_request": true}},
		{"pull_request", pullPayload("opened", false), audit.EventForgePullRequestOpened, "pull_request", "12", pull(false)},
		{"pull_request", pullPayload("reopened", false), audit.EventForgePullRequestReopened, "pull_request", "12", pull(false)},
		{"pull_request", pullPayload("synchronize", false), audit.EventForgePullRequestSynchronize, "pull_request", "12", pull(false)},
		{"pull_request", pullPayload("closed", true), audit.EventForgePullRequestMerged, "pull_request", "12", pull(true)},
		{"pull_request", pullPayload("closed", false), audit.EventForgePullRequestClosed, "pull_request", "12", pull(false)},
		{"push", githubPayload("", map[string]any{"ref": "refs/heads/main", "before": "1111", "after": "2222"}), audit.EventForgePush, "repository", "refs/heads/main",
			map[string]any{"ref": "refs/heads/main", "before": "1111", "after": "2222"}},
	} {
		events := relayed(t, forgeGitHub, tc.event, tc.body)
		if len(events) != 1 {
			t.Fatalf("%s %s: %d events; want 1", tc.event, tc.eventType, len(events))
		}
		ev := events[0]
		if ev.EventType != tc.eventType || ev.ResourceType != tc.resource || ev.ResourceID != tc.resourceID || ev.ActorType != "forge" || ev.ActorID != "" ||
			ev.Workspace != "widgets" || ev.Action != tc.eventType[strings.LastIndex(tc.eventType, ".")+1:] {
			t.Errorf("%s %s: event = %+v", tc.event, tc.eventType, ev)
		}
		if want := with(tc.metadata); !reflect.DeepEqual(ev.Metadata, want) {
			t.Errorf("%s %s: metadata = %#v\nwant %#v", tc.event, tc.eventType, ev.Metadata, want)
		}
	}
	for name, d := range map[string][2]string{
		"ping": {"ping", `{"zen":"x","repository":{"full_name":"acme/widgets"}}`}, "a pull request labeled": {"pull_request", pullPayload("labeled", false)},
		"a comment edited":  {"issue_comment", githubPayload("edited", map[string]any{"issue": map[string]any{"number": 3}})},
		"an issue assigned": {"issues", issuesPayload("assigned", "acme/widgets")},
	} {
		if red := reduce(forgeGitHub, d[0], d[1]); red.outcome != outcomeIgnored || red.target != targetOrigin || len(red.events) != 0 {
			t.Errorf("%s: %+v; want ignored, bound to the origin", name, red)
		}
	}
	noSender := strings.Replace(issuesPayload("closed", "acme/widgets"), `,"sender":{"login":"octocat"}`, "", 1)
	if events := relayed(t, forgeGitHub, "issues", noSender); events[0].Metadata["actor"] != "" {
		t.Errorf("without a sender the actor is %v; want empty", events[0].Metadata["actor"])
	}
	issueOf := func(fields map[string]any) map[string]any { return map[string]any{"issue": fields} }
	for name, d := range map[string][2]string{
		"an issue without a number":              {"issues", githubPayload("opened", issueOf(map[string]any{"title": "t"}))},
		"the number 0":                           {"issues", githubPayload("edited", issueOf(map[string]any{"number": 0}))},
		"a number as a string":                   {"issues", githubPayload("closed", issueOf(map[string]any{"number": "7"}))},
		"a number with a fraction":               {"issues", githubPayload("closed", issueOf(map[string]any{"number": 7.5}))},
		"labeled without a label":                {"issues", githubPayload("labeled", issueOf(map[string]any{"number": 7}))},
		"a comment without a number":             {"issue_comment", githubPayload("created", issueOf(map[string]any{"title": "t"}))},
		"a pull request without one":             {"pull_request", githubPayload("opened", nil)},
		"a closed pull request without a number": {"pull_request", githubPayload("closed", map[string]any{"pull_request": map[string]any{"merged": true}})},
		"a push without a ref":                   {"push", githubPayload("", map[string]any{"after": "2222"})},
		"a push with an empty ref":               {"push", githubPayload("", map[string]any{"ref": ""})},
		"a push with a numeric ref":              {"push", githubPayload("", map[string]any{"ref": 7})},
		"no repository":                          {"issues", `{"action":"opened","issue":{"number":7}}`},
		"two objects":                            {"issues", `{"a":1} {"b":2}`},
		"an array":                               {"issues", `[]`},
	} {
		if red := reduce(forgeGitHub, d[0], d[1]); red.outcome != outcomeMalformed || red.target != "" || len(red.events) != 0 {
			t.Errorf("%s: %+v; want malformed with no target", name, red)
		}
	}
}

// The GitLab hooks hub relays: an Issue Hook numbered by its iid with the top-level labels;
// an update as edited then one unlabeled and one labeled per change, sorted, each listing
// the issue's labels (the change's current ones when the top-level list is missing), a
// label listed twice once; a Note Hook on an issue or a merge request; a Merge Request Hook
// with merged for a merge or a merged state, and synchronize for an update with an oldrev;
// a Push Hook and a Tag Push Hook by the ref, with the push's user name.
func TestReduce_GitLab(t *testing.T) {
	issue := map[string]any{"forge": "gitlab", "target": "origin", "delivery_id": "d-1", "actor": "alice", "number": 7, "title": "Widgets break",
		"url": "https://gitlab.example.com/acme/widgets/-/issues/7", "labels": []string{"bug"}}
	for _, tc := range []struct{ action, eventType string }{{"open", audit.EventForgeIssueOpened}, {"close", audit.EventForgeIssueClosed}, {"reopen", audit.EventForgeIssueReopened}} {
		events := relayed(t, forgeGitLab, "Issue Hook", gitlabIssuePayload(tc.action, "acme/widgets"))
		if len(events) != 1 || events[0].EventType != tc.eventType || events[0].ResourceID != "7" || !reflect.DeepEqual(events[0].Metadata, issue) {
			t.Errorf("%s: events = %+v; want %s with %v", tc.action, events, tc.eventType, issue)
		}
	}
	events := relayed(t, forgeGitLab, "Issue Hook", gitlabUpdateHook(map[string]any{
		"title": map[string]any{"previous": "a", "current": "b"}, "labels": labelChange([]string{"z", "keep", "b", "b"}, []string{"y", "keep", "a", "a"})}, "keep", "y", "a"))
	var labels []string
	for _, ev := range events[1:] {
		labels = append(labels, ev.EventType[strings.LastIndex(ev.EventType, ".")+1:]+":"+ev.Metadata["label"].(string))
	}
	if events[0].EventType != audit.EventForgeIssueEdited || fmt.Sprint(labels) != "[unlabeled:b unlabeled:z labeled:a labeled:y]" || !reflect.DeepEqual(events[1].Metadata["labels"], []string{"keep", "y", "a"}) {
		t.Errorf("an update: %v with labels %v; want edited, then the changes sorted, each listing the issue's labels", eventTypes(events), labels)
	}
	noList := strings.Replace(gitlabUpdateHook(map[string]any{"labels": labelChange([]string{"x"}, []string{"y", "z"})}), `"labels":[]`, `"other":[]`, 1)
	if events := relayed(t, forgeGitLab, "Issue Hook", noList); len(events) != 3 || !reflect.DeepEqual(events[2].Metadata["labels"], []string{"y", "z"}) {
		t.Errorf("an update without the top-level labels: %v; want the change's current labels", events)
	}
	for name, body := range map[string]string{
		"no change": gitlabUpdateHook(map[string]any{}), "the same labels reordered": gitlabUpdateHook(map[string]any{"labels": labelChange([]string{"a", "b"}, []string{"b", "a"})}),
		"only assignees": gitlabUpdateHook(map[string]any{"assignees": []any{}}), "no changes object": gitlabPayload(map[string]any{"object_attributes": map[string]any{"iid": 7, "action": "update"}}),
	} {
		if red := reduce(forgeGitLab, "Issue Hook", body); red.outcome != outcomeIgnored || red.target != targetOrigin {
			t.Errorf("an update with %s: %+v; want ignored, bound to the origin", name, red)
		}
	}

	note := map[string]any{"forge": "gitlab", "target": "origin", "delivery_id": "d-1", "actor": "alice", "comment_url": "https://gitlab.example.com/acme/widgets/-/issues/7#note_1"}
	for _, tc := range []struct {
		body   string
		number int
		pull   bool
	}{
		{noteHook("Issue", "", map[string]any{"issue": map[string]any{"iid": 7}}), 7, false},
		{noteHook("MergeRequest", "create", map[string]any{"merge_request": map[string]any{"iid": 9}}), 9, true},
	} {
		note["number"], note["pull_request"] = tc.number, tc.pull
		events := relayed(t, forgeGitLab, "Note Hook", tc.body)
		if len(events) != 1 || events[0].EventType != audit.EventForgeCommentCreated || events[0].ResourceID != fmt.Sprint(tc.number) || !reflect.DeepEqual(events[0].Metadata, note) {
			t.Errorf("a note on %d: %+v; want comment.created with %v", tc.number, events, note)
		}
	}
	for name, body := range map[string]string{
		"an edited note": noteHook("Issue", "update", map[string]any{"issue": map[string]any{"iid": 7}}), "a note on a commit": noteHook("Commit", "", map[string]any{"commit": map[string]any{"id": "abc"}}),
		"a note on a snippet": noteHook("Snippet", "", nil), "a confidential note": `{"project":{"path_with_namespace":"acme/widgets"}}`,
	} {
		event := "Note Hook"
		if name == "a confidential note" {
			event = "Confidential Note Hook"
		}
		if red := reduce(forgeGitLab, event, body); red.outcome != outcomeIgnored {
			t.Errorf("%s: outcome = %q; want ignored", name, red.outcome)
		}
	}

	mr := func(merged bool) map[string]any {
		return map[string]any{"forge": "gitlab", "target": "origin", "delivery_id": "d-1", "actor": "alice", "number": 9,
			"url": "https://gitlab.example.com/acme/widgets/-/merge_requests/9", "head": "feature", "base": "main", "merged": merged}
	}
	for _, tc := range []struct {
		body      string
		eventType string
		merged    bool
	}{
		{mergeRequestHook("open", nil), audit.EventForgePullRequestOpened, false},
		{mergeRequestHook("close", nil), audit.EventForgePullRequestClosed, false},
		{mergeRequestHook("reopen", nil), audit.EventForgePullRequestReopened, false},
		{mergeRequestHook("merge", nil), audit.EventForgePullRequestMerged, true},
		{mergeRequestHook("update", map[string]any{"oldrev": "abc", "state": "merged"}), audit.EventForgePullRequestSynchronize, true},
		{mergeRequestHook("update", map[string]any{"oldrev": "abc"}), audit.EventForgePullRequestSynchronize, false},
	} {
		events := relayed(t, forgeGitLab, "Merge Request Hook", tc.body)
		if len(events) != 1 || events[0].EventType != tc.eventType || events[0].ResourceType != "pull_request" || events[0].ResourceID != "9" || !reflect.DeepEqual(events[0].Metadata, mr(tc.merged)) {
			t.Errorf("%s: %+v; want %s with merged %v", tc.body, events, tc.eventType, tc.merged)
		}
	}
	if red := reduce(forgeGitLab, "Merge Request Hook", mergeRequestHook("update", nil)); red.outcome != outcomeIgnored {
		t.Errorf("an update without an oldrev: outcome = %q; want ignored", red.outcome)
	}

	for _, tc := range []struct{ event, ref string }{{"Push Hook", "refs/heads/main"}, {"Tag Push Hook", "refs/tags/v1"}} {
		body := gitlabPayload(map[string]any{"ref": tc.ref, "before": "1111", "after": "2222", "user_username": "bob", "user": nil})
		events := relayed(t, forgeGitLab, tc.event, body)
		want := map[string]any{"forge": "gitlab", "target": "origin", "delivery_id": "d-1", "actor": "bob", "ref": tc.ref, "before": "1111", "after": "2222"}
		if len(events) != 1 || events[0].EventType != audit.EventForgePush || events[0].ResourceType != "repository" || events[0].ResourceID != tc.ref || !reflect.DeepEqual(events[0].Metadata, want) {
			t.Errorf("%s: %+v; want a push of %s", tc.event, events, tc.ref)
		}
	}
	for name, d := range map[string][2]string{
		"an issue without an iid":        {"Issue Hook", gitlabPayload(map[string]any{"object_attributes": map[string]any{"action": "open"}})},
		"an update without an iid":       {"Issue Hook", gitlabPayload(map[string]any{"object_attributes": map[string]any{"action": "update"}, "changes": map[string]any{"labels": labelChange([]string{"x"}, []string{"y"})}})},
		"a note without its issue":       {"Note Hook", noteHook("Issue", "", nil)},
		"a merge request without an iid": {"Merge Request Hook", gitlabPayload(map[string]any{"object_attributes": map[string]any{"action": "open"}})},
		"a push without a ref":           {"Push Hook", gitlabPayload(nil)}, "a push with a numeric ref": {"Push Hook", gitlabPayload(map[string]any{"ref": 5})},
	} {
		if red := reduce(forgeGitLab, d[0], d[1]); red.outcome != outcomeMalformed {
			t.Errorf("%s: outcome = %q; want malformed", name, red.outcome)
		}
	}
	for _, event := range []string{"Confidential Issue Hook", "Pipeline Hook"} {
		if red := reduce(forgeGitLab, event, gitlabIssuePayload("open", "acme/widgets")); red.outcome != outcomeIgnored {
			t.Errorf("%s: outcome = %q; want ignored", event, red.outcome)
		}
	}
}

// Every relayed event carries exactly the metadata keys of its kind and nothing else of the
// payload; titles are cut to 256 bytes on a UTF-8 boundary and URLs lose their userinfo.
func TestReduce_EventContentsAreBounded(t *testing.T) {
	const marker = "PAYLOAD-ONLY-MARKER"
	extra := func(doc string) string {
		return doc[:len(doc)-1] + `,"leaked":"` + marker + `","sender2":{"email":"` + marker + `"}}`
	}
	for _, tc := range []struct {
		forge, event, body string
		keys               []string
	}{
		{forgeGitHub, "issues", extra(issuesPayload("edited", "acme/widgets")), []string{"number", "title", "url", "labels"}},
		{forgeGitHub, "issues", extra(issuesPayload("labeled", "acme/widgets")), []string{"number", "label", "labels", "url"}},
		{forgeGitHub, "issue_comment", extra(githubPayload("created", map[string]any{"issue": map[string]any{"number": 7}})), []string{"number", "comment_url", "pull_request"}},
		{forgeGitHub, "pull_request", extra(pullPayload("closed", true)), []string{"number", "url", "head", "base", "merged"}},
		{forgeGitHub, "push", extra(githubPayload("", map[string]any{"ref": "refs/heads/main"})), []string{"ref", "before", "after"}},
		{forgeGitLab, "Issue Hook", extra(gitlabUpdateHook(map[string]any{"labels": labelChange([]string{"x"}, []string{"y"})}, "y")), []string{"number", "label", "labels", "url"}},
	} {
		for _, ev := range relayed(t, tc.forge, tc.event, tc.body) {
			want := append([]string{"actor", "delivery_id", "forge", "target"}, tc.keys...)
			sort.Strings(want)
			var got []string
			for k := range ev.Metadata {
				got = append(got, k)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) || strings.Contains(marshal(ev), marker) {
				t.Errorf("%s %s: %s has the keys %v; want %v and nothing of the payload", tc.forge, tc.event, ev.EventType, got, want)
			}
		}
	}
	for _, title := range []string{strings.Repeat("a", 300), strings.Repeat("é", 200), strings.Repeat("世", 100), strings.Repeat("😀", 70), "short", "x\xffy"} {
		body := githubPayload("opened", map[string]any{"issue": map[string]any{"number": 7, "title": title, "html_url": "https://x-token-auth:ghp_0123@github.com/acme/widgets/issues/7"}})
		ev := relayed(t, forgeGitHub, "issues", body)[0]
		got, _ := ev.Metadata["title"].(string)
		if !utf8.ValidString(got) || len(got) > 256 || (len(title) <= 256 && utf8.ValidString(title) && got != title) || (len(title) > 256 && len(got) < 253) {
			t.Errorf("the title %q (%d bytes) became %q (%d bytes); want a valid prefix of at most 256 bytes", title, len(title), got, len(got))
		}
		if ev.Metadata["url"] != "https://github.com/acme/widgets/issues/7" {
			t.Errorf("url = %v; want it without userinfo", ev.Metadata["url"])
		}
	}
}

// An event lists at most 100 labels and a GitLab update relays at most 100 label changes.
func TestReduce_LabelsAreBounded(t *testing.T) {
	logs := captureLog(t)
	many := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s%03d", prefix, i)
		}
		return out
	}
	var ghLabels []any
	for _, name := range many("l", 150) {
		ghLabels = append(ghLabels, map[string]any{"name": name})
	}
	gh := relayed(t, forgeGitHub, "issues", githubPayload("opened", map[string]any{"issue": map[string]any{"number": 7, "labels": ghLabels}}))
	gl := relayed(t, forgeGitLab, "Issue Hook", gitlabPayload(map[string]any{"object_attributes": map[string]any{"iid": 7, "action": "open"}, "labels": titles(many("l", 150)...)}))
	if !reflect.DeepEqual(gh[0].Metadata["labels"], many("l", 100)) || !reflect.DeepEqual(gl[0].Metadata["labels"], many("l", 100)) {
		t.Errorf("labels = %d and %d names; want the first 100 on both forges", len(gh[0].Metadata["labels"].([]string)), len(gl[0].Metadata["labels"].([]string)))
	}
	for _, tc := range []struct{ removed, added, wantUnlabeled, wantLabeled int }{{60, 40, 60, 40}, {60, 41, 60, 40}, {3, 500, 3, 97}} {
		current := many("n", tc.added)
		events := relayed(t, forgeGitLab, "Issue Hook", gitlabUpdateHook(map[string]any{"labels": labelChange(many("p", tc.removed), current)}, current...))
		unlabeled, labeled := 0, 0
		for _, ev := range events {
			switch ev.EventType {
			case audit.EventForgeIssueUnlabeled:
				unlabeled++
			case audit.EventForgeIssueLabeled:
				labeled++
			}
		}
		if unlabeled != tc.wantUnlabeled || labeled != tc.wantLabeled {
			t.Errorf("%d removed and %d added: %d unlabeled and %d labeled events; want %d and %d", tc.removed, tc.added, unlabeled, labeled, tc.wantUnlabeled, tc.wantLabeled)
		}
	}
	if strings.Count(logs.String(), "changed more labels") != 2 {
		t.Errorf("the cut was logged %d times; want 2:\n%s", strings.Count(logs.String(), "changed more labels"), logs)
	}
}
